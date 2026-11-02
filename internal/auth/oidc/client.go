package oidc

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	gooidc "github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"

	"github.com/vinRosso/hogwatch/internal/config"
)

var (
	// ErrUnavailable means the identity provider could not be reached or
	// answered 5xx. Callers may tell the user to try again later.
	ErrUnavailable = errors.New("oidc: identity provider unavailable")
	// ErrFailed means the login cannot be trusted: a rejected code, a token
	// that fails verification, a subject mismatch. The wrapped text is a fixed
	// phrase; the underlying cause (which may echo a code or secret) is dropped.
	ErrFailed = errors.New("oidc: login failed")
)

func failed(what string) error      { return fmt.Errorf("%w: %s", ErrFailed, what) }
func unavailable(what string) error { return fmt.Errorf("%w: %s", ErrUnavailable, what) }

var scopes = []string{gooidc.ScopeOpenID, "profile", "email"}

// Client talks to one issuer. Discovery is lazy and cached on success only,
// so the web server starts while the provider is down.
type Client struct {
	cfg      config.AuthOIDC
	redirect string
	hc       *http.Client

	mu sync.Mutex
	d  *discovered
}

type discovered struct {
	provider *gooidc.Provider
	oauth    *oauth2.Config
	verifier *gooidc.IDTokenVerifier
}

// New builds a client; nothing is fetched until the first use.
func New(cfg config.AuthOIDC, redirectURL string, hc *http.Client) *Client {
	if hc == nil {
		hc = &http.Client{Timeout: 10 * time.Second}
	}
	return &Client{cfg: cfg, redirect: redirectURL, hc: hc}
}

func (c *Client) discover(ctx context.Context) (*discovered, error) {
	c.mu.Lock()
	d := c.d
	c.mu.Unlock()
	if d != nil {
		return d, nil
	}
	// The lock is not held across the network call: a hung provider must not
	// queue every login behind one slow discovery. Two racing first calls both
	// discover; the first result stays.
	p, err := gooidc.NewProvider(gooidc.ClientContext(ctx, c.hc), c.cfg.Issuer)
	if err != nil {
		return nil, unavailable("discovery")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.d != nil {
		return c.d, nil
	}
	c.d = &discovered{
		provider: p,
		oauth: &oauth2.Config{
			ClientID: c.cfg.ClientID, ClientSecret: c.cfg.ClientSecret,
			Endpoint: p.Endpoint(), RedirectURL: c.redirect, Scopes: scopes,
		},
		verifier: p.Verifier(&gooidc.Config{ClientID: c.cfg.ClientID}),
	}
	return c.d, nil
}

// AuthURL is where to send the browser: the code flow with PKCE S256, the
// state that binds the callback to this browser and the nonce that binds the
// ID token to this flow.
func (c *Client) AuthURL(ctx context.Context, state, nonce, verifier string) (string, error) {
	d, err := c.discover(ctx)
	if err != nil {
		return "", err
	}
	return d.oauth.AuthCodeURL(state, oauth2.S256ChallengeOption(verifier), gooidc.Nonce(nonce)), nil
}

// Exchange trades the code for tokens (sending the PKCE verifier), verifies
// the ID token (signature, issuer, audience, expiry, nonce) and returns the
// identity. Email and groups missing from the ID token come from UserInfo,
// which must be about the same subject.
func (c *Client) Exchange(ctx context.Context, code, verifier, nonce string) (Identity, error) {
	d, err := c.discover(ctx)
	if err != nil {
		return Identity{}, err
	}
	ctx = gooidc.ClientContext(ctx, c.hc)

	tok, err := d.oauth.Exchange(ctx, code, oauth2.VerifierOption(verifier))
	if err != nil {
		var re *oauth2.RetrieveError
		if !errors.As(err, &re) || re.Response == nil || re.Response.StatusCode >= http.StatusInternalServerError {
			return Identity{}, unavailable("token exchange")
		}
		return Identity{}, failed("token exchange")
	}
	raw, _ := tok.Extra("id_token").(string)
	if raw == "" {
		return Identity{}, failed("no id_token")
	}
	idTok, err := d.verifier.Verify(ctx, raw)
	if err != nil {
		return Identity{}, failed("id_token verification")
	}
	if subtle.ConstantTimeCompare([]byte(idTok.Nonce), []byte(nonce)) != 1 || nonce == "" {
		return Identity{}, failed("nonce")
	}
	if idTok.Subject == "" {
		return Identity{}, failed("no subject")
	}
	if len(idTok.Subject) > MaxSubject {
		return Identity{}, failed("subject too long")
	}
	var claims map[string]any
	if err := idTok.Claims(&claims); err != nil {
		return Identity{}, failed("id_token claims")
	}
	id := identityFrom(idTok.Issuer, idTok.Subject, claims, c.cfg.GroupsClaim, c.cfg.AdminGroup)

	_, haveGroups := claims[c.cfg.GroupsClaim]
	needEmail := id.Email == ""
	needGroups := !haveGroups && c.cfg.AdminGroup != ""
	if !needEmail && !needGroups {
		return id, nil
	}
	ui, err := d.provider.UserInfo(ctx, oauth2.StaticTokenSource(tok))
	if err != nil {
		return Identity{}, failed("userinfo")
	}
	if ui.Subject != id.Subject {
		return Identity{}, failed("userinfo subject")
	}
	var uiClaims map[string]any
	if err := ui.Claims(&uiClaims); err != nil {
		return Identity{}, failed("userinfo claims")
	}
	fromUI := identityFrom(id.Issuer, id.Subject, uiClaims, c.cfg.GroupsClaim, c.cfg.AdminGroup)
	if needEmail {
		id.Email, id.EmailVerified = fromUI.Email, fromUI.EmailVerified
	}
	if needGroups {
		id.Groups, id.GroupsAdmin = fromUI.Groups, fromUI.GroupsAdmin
	}
	if id.PreferredUsername == "" {
		id.PreferredUsername = fromUI.PreferredUsername
	}
	if id.Name == "" {
		id.Name = fromUI.Name
	}
	return id, nil
}
