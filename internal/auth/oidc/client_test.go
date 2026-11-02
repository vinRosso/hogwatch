package oidc

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/vinRosso/hogwatch/internal/auth/oidc/oidctest"
	"github.com/vinRosso/hogwatch/internal/config"
)

const redirectURL = "https://hog.example/api/v1/auth/oidc/callback"

var testVerifier = "v" + strings.Repeat("e", 63)

func newClient(t *testing.T, iss *oidctest.Issuer, edit ...func(*config.AuthOIDC)) *Client {
	t.Helper()
	cfg := config.AuthOIDC{
		Issuer: iss.URL, ClientID: iss.ClientID, ClientSecret: iss.ClientSecret,
		GroupsClaim: "groups", AdminGroup: "admins",
	}
	for _, e := range edit {
		e(&cfg)
	}
	iss.Register(redirectURL)
	return New(cfg, redirectURL, &http.Client{Timeout: 5 * time.Second})
}

// flow walks start -> browser -> callback and returns the pieces Exchange needs.
type flow struct{ code, state, nonce, verifier string }

func begin(t *testing.T, c *Client, iss *oidctest.Issuer) flow {
	t.Helper()
	f := flow{state: "state-1", nonce: "nonce-1", verifier: testVerifier}
	authURL, err := c.AuthURL(context.Background(), f.state, f.nonce, f.verifier)
	if err != nil {
		t.Fatalf("AuthURL: %v", err)
	}
	cb, err := url.Parse(iss.Authorize(t, authURL))
	if err != nil {
		t.Fatal(err)
	}
	if got := cb.Query().Get("state"); got != f.state {
		t.Fatalf("state came back as %q", got)
	}
	f.code = cb.Query().Get("code")
	if f.code == "" {
		t.Fatalf("no code in %s", cb)
	}
	return f
}

func TestClient_LoginRoundTrip(t *testing.T) {
	iss := oidctest.New(t)
	iss.Next(map[string]any{
		"sub": "sub-7", "email": "alice@example.com", "email_verified": true,
		"preferred_username": "alice", "name": "Alice", "groups": []string{"admins"},
	})
	c := newClient(t, iss)
	authURL, err := c.AuthURL(context.Background(), "st", "no", testVerifier)
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(authURL)
	q := u.Query()
	if q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" ||
		q.Get("state") != "st" || q.Get("nonce") != "no" || q.Get("redirect_uri") != redirectURL ||
		q.Get("client_id") != iss.ClientID || q.Get("response_type") != "code" ||
		!strings.Contains(q.Get("scope"), "openid") || !strings.Contains(q.Get("scope"), "email") {
		t.Fatalf("auth URL query = %v", q)
	}

	f := begin(t, c, iss)
	id, err := c.Exchange(context.Background(), f.code, f.verifier, f.nonce)
	if err != nil {
		t.Fatalf("Exchange: %v", err)
	}
	if id.Issuer != iss.URL || id.Subject != "sub-7" || id.Email != "alice@example.com" || !id.EmailVerified ||
		id.PreferredUsername != "alice" || id.Name != "Alice" || !id.GroupsAdmin {
		t.Fatalf("identity = %+v", id)
	}
}

// The negative control for the mock: it must refuse a wrong verifier, or the
// round trip above proves nothing about PKCE.
func TestClient_PKCEEnforced(t *testing.T) {
	iss := oidctest.New(t)
	c := newClient(t, iss)
	f := begin(t, c, iss)
	if _, err := c.Exchange(context.Background(), f.code, "w"+strings.Repeat("r", 63), f.nonce); !errors.Is(err, ErrFailed) {
		t.Fatalf("wrong verifier: err = %v, want ErrFailed", err)
	}
	// The failed attempt spent the code, so even the right verifier fails now.
	if _, err := c.Exchange(context.Background(), f.code, f.verifier, f.nonce); !errors.Is(err, ErrFailed) {
		t.Fatalf("replay: err = %v, want ErrFailed", err)
	}
}

func TestClient_CodeReplay(t *testing.T) {
	iss := oidctest.New(t)
	c := newClient(t, iss)
	f := begin(t, c, iss)
	if _, err := c.Exchange(context.Background(), f.code, f.verifier, f.nonce); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Exchange(context.Background(), f.code, f.verifier, f.nonce); !errors.Is(err, ErrFailed) {
		t.Fatalf("replayed code: err = %v, want ErrFailed", err)
	}
}

func TestClient_WrongClientSecret(t *testing.T) {
	iss := oidctest.New(t)
	c := newClient(t, iss, func(a *config.AuthOIDC) { a.ClientSecret = "nope" })
	f := begin(t, c, iss)
	if _, err := c.Exchange(context.Background(), f.code, f.verifier, f.nonce); !errors.Is(err, ErrFailed) {
		t.Fatalf("err = %v, want ErrFailed", err)
	}
}

func TestClient_RejectsBadTokens(t *testing.T) {
	past := time.Now().Add(-time.Hour).Unix()
	rows := []struct {
		name    string
		mutate  func(map[string]any)
		foreign bool
		nonce   string // what the client expects; "" means the flow's nonce
	}{
		{"wrong issuer", func(m map[string]any) { m["iss"] = "https://evil.example" }, false, ""},
		{"wrong audience", func(m map[string]any) { m["aud"] = "someone-else" }, false, ""},
		{"expired", func(m map[string]any) { m["exp"] = past; m["iat"] = past - 60 }, false, ""},
		{"wrong nonce in token", func(m map[string]any) { m["nonce"] = "other" }, false, ""},
		{"missing nonce in token", func(m map[string]any) { delete(m, "nonce") }, false, ""},
		{"foreign signing key", nil, true, ""},
		{"client expects another nonce", nil, false, "not-the-one-sent"},
	}
	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			iss := oidctest.New(t)
			c := newClient(t, iss)
			f := begin(t, c, iss)
			iss.SetTokenMutator(r.mutate)
			iss.SignWithForeignKey(r.foreign)
			nonce := f.nonce
			if r.nonce != "" {
				nonce = r.nonce
			}
			if _, err := c.Exchange(context.Background(), f.code, f.verifier, nonce); !errors.Is(err, ErrFailed) {
				t.Fatalf("err = %v, want ErrFailed", err)
			}
		})
	}
}

func TestClient_DiscoveryRetry(t *testing.T) {
	iss := oidctest.New(t)
	c := newClient(t, iss)
	iss.Down(true)
	if _, err := c.AuthURL(context.Background(), "s", "n", testVerifier); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("down: err = %v, want ErrUnavailable", err)
	}
	iss.Down(false)
	if _, err := c.AuthURL(context.Background(), "s", "n", testVerifier); err != nil {
		t.Fatalf("after recovery the failure was cached: %v", err)
	}
}

func TestClient_TokenEndpointDown(t *testing.T) {
	iss := oidctest.New(t)
	c := newClient(t, iss)
	f := begin(t, c, iss)
	iss.Down(true)
	if _, err := c.Exchange(context.Background(), f.code, f.verifier, f.nonce); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err = %v, want ErrUnavailable", err)
	}
}

func TestClient_UserInfoFallback(t *testing.T) {
	iss := oidctest.New(t)
	iss.Next(map[string]any{"sub": "u1"}) // no email, no groups in the ID token
	iss.SetUserInfo(map[string]any{"email": "bob@example.com", "email_verified": true, "groups": []string{"admins"}})
	c := newClient(t, iss)
	f := begin(t, c, iss)
	id, err := c.Exchange(context.Background(), f.code, f.verifier, f.nonce)
	if err != nil {
		t.Fatal(err)
	}
	if id.Email != "bob@example.com" || !id.EmailVerified || !id.GroupsAdmin {
		t.Fatalf("identity = %+v", id)
	}
}

func TestClient_UserInfoNeverOverridesToken(t *testing.T) {
	iss := oidctest.New(t)
	iss.Next(map[string]any{"sub": "u1", "email": "a@x.example", "email_verified": false, "groups": []string{"staff"}})
	// UserInfo says otherwise: it must not override what the ID token carries.
	iss.SetUserInfo(map[string]any{"email": "evil@x.example", "email_verified": true, "groups": []string{"admins"}})
	c := newClient(t, iss)
	f := begin(t, c, iss)
	id, err := c.Exchange(context.Background(), f.code, f.verifier, f.nonce)
	if err != nil {
		t.Fatal(err)
	}
	if id.Email != "a@x.example" || id.EmailVerified || id.GroupsAdmin {
		t.Fatalf("UserInfo overrode the ID token: %+v", id)
	}
}

func TestClient_UserInfoSubjectMismatch(t *testing.T) {
	iss := oidctest.New(t)
	iss.Next(map[string]any{"sub": "u1"})
	iss.SetUserInfo(map[string]any{"sub": "someone-else", "email": "x@y.example", "email_verified": true, "groups": []string{"admins"}})
	c := newClient(t, iss)
	f := begin(t, c, iss)
	if _, err := c.Exchange(context.Background(), f.code, f.verifier, f.nonce); !errors.Is(err, ErrFailed) {
		t.Fatalf("err = %v, want ErrFailed", err)
	}
}

func TestClient_NoSubject(t *testing.T) {
	iss := oidctest.New(t)
	iss.Next(map[string]any{"sub": ""})
	c := newClient(t, iss)
	f := begin(t, c, iss)
	if _, err := c.Exchange(context.Background(), f.code, f.verifier, f.nonce); !errors.Is(err, ErrFailed) {
		t.Fatalf("empty sub: err = %v, want ErrFailed", err)
	}
}

// The provider echoes everything it was sent in its error body; the client
// must not carry that text into its own errors (they reach logs).
func TestClient_ErrorsHideCause(t *testing.T) {
	iss := oidctest.New(t)
	iss.EchoSecrets(true)
	wrong := "w" + strings.Repeat("r", 63)
	c := newClient(t, iss)
	f := begin(t, c, iss)
	_, err := c.Exchange(context.Background(), f.code, wrong, f.nonce)
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, leak := range []string{iss.ClientSecret, f.code, wrong, "rejected"} {
		if strings.Contains(err.Error(), leak) {
			t.Fatalf("error %q leaks %q", err, leak)
		}
	}
}

// Negative control for the test above: with EchoSecrets the provider body does
// hold the secret, so a leak would be visible.
func TestIssuer_EchoSecretsControl(t *testing.T) {
	iss := oidctest.New(t)
	iss.EchoSecrets(true)
	form := url.Values{"grant_type": {"authorization_code"}, "code": {"c0de"}, "code_verifier": {"verif"},
		"client_id": {iss.ClientID}, "client_secret": {iss.ClientSecret}}
	resp, err := http.PostForm(iss.URL+"/token", form)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var b strings.Builder
	_, _ = io.Copy(&b, resp.Body)
	if !strings.Contains(b.String(), iss.ClientSecret) {
		t.Fatalf("echo mode does not echo: %s", b.String())
	}
}

func TestIssuer_RejectsUnregisteredRedirect(t *testing.T) {
	iss := oidctest.New(t)
	c := newClient(t, iss)
	other := New(config.AuthOIDC{Issuer: iss.URL, ClientID: iss.ClientID, ClientSecret: iss.ClientSecret, GroupsClaim: "groups"},
		"https://evil.example/cb", nil)
	authURL, err := other.AuthURL(context.Background(), "s", "n", testVerifier)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := (&http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}).Get(authURL)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("unregistered redirect_uri answered %d, want 400", resp.StatusCode)
	}
	_ = c
}
