// Package oidctest is an in-process OpenID Provider for tests. It enforces
// what the real flow relies on: single-use codes, the client secret, the
// redirect URI and the PKCE S256 verifier, so a test that passes proves the
// client sent them.
package oidctest

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"maps"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

const kid = "k1"

var (
	keysOnce        sync.Once
	goodKey, badKey *rsa.PrivateKey
	keysErr         error
	b64             = base64.RawURLEncoding
)

func keys() (*rsa.PrivateKey, *rsa.PrivateKey) {
	keysOnce.Do(func() {
		if goodKey, keysErr = rsa.GenerateKey(rand.Reader, 2048); keysErr != nil {
			return
		}
		badKey, keysErr = rsa.GenerateKey(rand.Reader, 2048)
	})
	return goodKey, badKey
}

type grant struct {
	claims    map[string]any
	challenge string
	redirect  string
	nonce     string
	used      bool
}

// Issuer is a running mock provider. Its knobs are safe to set while it serves.
type Issuer struct {
	URL          string
	ClientID     string
	ClientSecret string

	srv  *httptest.Server
	good *rsa.PrivateKey
	bad  *rsa.PrivateKey

	mu       sync.Mutex
	down     bool
	next     map[string]any
	userInfo map[string]any
	mutator  func(claims map[string]any)
	foreign  bool
	denyNext string
	redirect map[string]bool // registered redirect URIs
	echo     bool            // put the submitted secrets in error bodies
	codes    map[string]*grant
	tokens   map[string]string // access token -> subject
	counter  int
	tokenReq int // requests that reached /token
}

// New starts a provider and stops it with the test.
func New(t testing.TB) *Issuer {
	t.Helper()
	good, bad := keys()
	if keysErr != nil {
		t.Fatal(keysErr)
	}
	i := &Issuer{
		ClientID: "hogwatch-test", ClientSecret: "test-secret-VALUE",
		good: good, bad: bad,
		codes: map[string]*grant{}, tokens: map[string]string{},
		next: map[string]any{"sub": "sub-default"},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/openid-configuration", i.gate(i.discovery))
	mux.HandleFunc("GET /jwks", i.gate(i.jwks))
	mux.HandleFunc("GET /authorize", i.gate(i.authorize))
	mux.HandleFunc("POST /token", i.gate(i.token))
	mux.HandleFunc("GET /userinfo", i.gate(i.userinfo))
	i.srv = httptest.NewServer(mux)
	i.URL = i.srv.URL
	t.Cleanup(i.srv.Close)
	return i
}

// TokenRequests is how many requests have reached the token endpoint.
func (i *Issuer) TokenRequests() int {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.tokenReq
}

// Next sets the claims of the next authorization. "sub" is required; iss, aud,
// exp, iat and nonce are added by the issuer.
func (i *Issuer) Next(claims map[string]any) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.next = maps.Clone(claims)
}

// SetUserInfo sets the /userinfo document. A missing "sub" is filled with the
// subject of the access token.
func (i *Issuer) SetUserInfo(m map[string]any) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.userInfo = maps.Clone(m)
}

// Register adds a redirect URI the client may use. /authorize rejects any other.
func (i *Issuer) Register(uri string) {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.redirect == nil {
		i.redirect = map[string]bool{}
	}
	i.redirect[uri] = true
}

// EchoSecrets makes /token error bodies repeat the submitted client secret,
// code and verifier, as a careless provider might. A client that wraps such a
// body into its own errors then leaks them, which its tests can catch.
func (i *Issuer) EchoSecrets(v bool) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.echo = v
}

// Down makes every endpoint answer 503.
func (i *Issuer) Down(v bool) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.down = v
}

// SetTokenMutator corrupts the ID-token claims just before signing.
func (i *Issuer) SetTokenMutator(f func(claims map[string]any)) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.mutator = f
}

// SignWithForeignKey signs ID tokens with a key the JWKS does not publish.
func (i *Issuer) SignWithForeignKey(v bool) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.foreign = v
}

// DenyNext makes the next /authorize answer with the OAuth error code, as when
// the user cancels at the provider.
func (i *Issuer) DenyNext(code string) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.denyNext = code
}

func (i *Issuer) gate(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		i.mu.Lock()
		down := i.down
		i.mu.Unlock()
		if down {
			http.Error(w, "down", http.StatusServiceUnavailable)
			return
		}
		h(w, r)
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (i *Issuer) discovery(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"issuer":                                i.URL,
		"authorization_endpoint":                i.URL + "/authorize",
		"token_endpoint":                        i.URL + "/token",
		"jwks_uri":                              i.URL + "/jwks",
		"userinfo_endpoint":                     i.URL + "/userinfo",
		"response_types_supported":              []string{"code"},
		"subject_types_supported":               []string{"public"},
		"id_token_signing_alg_values_supported": []string{"RS256"},
	})
}

func (i *Issuer) jwks(w http.ResponseWriter, _ *http.Request) {
	pub := i.good.PublicKey
	writeJSON(w, http.StatusOK, map[string]any{"keys": []map[string]string{{
		"kty": "RSA", "use": "sig", "alg": "RS256", "kid": kid,
		"n": b64.EncodeToString(pub.N.Bytes()),
		"e": b64.EncodeToString(big.NewInt(int64(pub.E)).Bytes()),
	}}})
}

func (i *Issuer) authorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	redirect, state := q.Get("redirect_uri"), q.Get("state")
	bad := func(msg string) { http.Error(w, "authorize: "+msg, http.StatusBadRequest) }
	switch {
	case q.Get("client_id") != i.ClientID:
		bad("client_id")
	case q.Get("response_type") != "code":
		bad("response_type")
	case redirect == "" || state == "":
		bad("redirect_uri and state are required")
	case !i.registered(redirect):
		bad("redirect_uri is not registered")
	case q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "":
		bad("PKCE S256 is required")
	case q.Get("nonce") == "":
		bad("nonce is required")
	case !strings.Contains(q.Get("scope"), "openid"):
		bad("scope must include openid")
	default:
		u, err := url.Parse(redirect)
		if err != nil {
			bad("redirect_uri")
			return
		}
		out := u.Query()
		out.Set("state", state)
		i.mu.Lock()
		if i.denyNext != "" {
			out.Set("error", i.denyNext)
			i.denyNext = ""
		} else {
			i.counter++
			code := fmt.Sprintf("code-%d-%s", i.counter, randString())
			i.codes[code] = &grant{
				claims: maps.Clone(i.next), challenge: q.Get("code_challenge"),
				redirect: redirect, nonce: q.Get("nonce"),
			}
			out.Set("code", code)
		}
		i.mu.Unlock()
		u.RawQuery = out.Encode()
		http.Redirect(w, r, u.String(), http.StatusFound) //nolint:gosec // test provider: it redirects to the redirect_uri the test client sent
	}
}

func (i *Issuer) registered(uri string) bool {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.redirect[uri]
}

func randString() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return b64.EncodeToString(b)
}

func (i *Issuer) token(w http.ResponseWriter, r *http.Request) {
	i.mu.Lock()
	i.tokenReq++
	i.mu.Unlock()
	if err := r.ParseForm(); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_request"})
		return
	}
	i.mu.Lock()
	echo := i.echo
	i.mu.Unlock()
	oauthErr := func(status int, code string) {
		body := map[string]string{"error": code}
		if echo {
			body["error_description"] = fmt.Sprintf("rejected code=%s verifier=%s secret=%s",
				r.PostForm.Get("code"), r.PostForm.Get("code_verifier"), r.PostForm.Get("client_secret"))
		}
		writeJSON(w, status, body)
	}
	id, secret, basic := r.BasicAuth()
	if !basic {
		id, secret = r.PostForm.Get("client_id"), r.PostForm.Get("client_secret")
	} else {
		// RFC 6749 §2.3.1: the Basic credentials are form-urlencoded first.
		if u, err := url.QueryUnescape(id); err == nil {
			id = u
		}
		if s, err := url.QueryUnescape(secret); err == nil {
			secret = s
		}
	}
	if id != i.ClientID || subtle.ConstantTimeCompare([]byte(secret), []byte(i.ClientSecret)) != 1 {
		oauthErr(http.StatusUnauthorized, "invalid_client")
		return
	}
	if r.PostForm.Get("grant_type") != "authorization_code" {
		oauthErr(http.StatusBadRequest, "unsupported_grant_type")
		return
	}

	i.mu.Lock()
	g, ok := i.codes[r.PostForm.Get("code")]
	if ok {
		replay := g.used
		g.used = true
		ok = !replay
	}
	mutator, foreign := i.mutator, i.foreign
	i.mu.Unlock()
	if !ok {
		oauthErr(http.StatusBadRequest, "invalid_grant")
		return
	}
	verifier := r.PostForm.Get("code_verifier")
	sum := sha256.Sum256([]byte(verifier))
	if verifier == "" || b64.EncodeToString(sum[:]) != g.challenge {
		oauthErr(http.StatusBadRequest, "invalid_grant")
		return
	}
	if r.PostForm.Get("redirect_uri") != g.redirect {
		oauthErr(http.StatusBadRequest, "invalid_grant")
		return
	}

	now := time.Now()
	claims := maps.Clone(g.claims)
	claims["iss"], claims["aud"], claims["nonce"] = i.URL, i.ClientID, g.nonce
	claims["iat"], claims["exp"] = now.Unix(), now.Add(5*time.Minute).Unix()
	if mutator != nil {
		mutator(claims)
	}
	key := i.good
	if foreign {
		key = i.bad
	}
	sub, _ := g.claims["sub"].(string)
	access := "at-" + randString()
	i.mu.Lock()
	i.tokens[access] = sub
	i.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{
		"access_token": access, "token_type": "Bearer", "expires_in": 300,
		"id_token": sign(key, claims),
	})
}

func (i *Issuer) userinfo(w http.ResponseWriter, r *http.Request) {
	access := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	i.mu.Lock()
	sub, ok := i.tokens[access]
	doc := maps.Clone(i.userInfo)
	i.mu.Unlock()
	if !ok {
		http.Error(w, "bad token", http.StatusUnauthorized)
		return
	}
	if doc == nil {
		doc = map[string]any{}
	}
	if _, has := doc["sub"]; !has {
		doc["sub"] = sub
	}
	writeJSON(w, http.StatusOK, doc)
}

func sign(key *rsa.PrivateKey, claims map[string]any) string {
	hdr, _ := json.Marshal(map[string]string{"alg": "RS256", "typ": "JWT", "kid": kid})
	pl, _ := json.Marshal(claims)
	in := b64.EncodeToString(hdr) + "." + b64.EncodeToString(pl)
	h := sha256.Sum256([]byte(in))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, h[:])
	if err != nil {
		panic(err)
	}
	return in + "." + b64.EncodeToString(sig)
}

// Authorize stands in for the browser: it opens the authorization URL without
// following redirects and returns where the provider sent the browser next.
func (i *Issuer) Authorize(t testing.TB, authURL string) string {
	t.Helper()
	c := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := c.Get(authURL)
	if err != nil {
		t.Fatalf("authorize: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("authorize answered %d, want 302", resp.StatusCode)
	}
	return resp.Header.Get("Location")
}
