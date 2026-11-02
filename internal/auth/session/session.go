// Package session implements server-side sessions. The cookie
// carries 256 random bits; the database stores only their SHA-256.
package session

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/vinRosso/hogwatch/internal/config"
	"github.com/vinRosso/hogwatch/internal/db/dbq"
)

// CookieName is the session cookie. The __Host- prefix makes browsers insist
// on Secure, Path=/ and no Domain.
const CookieName = "__Host-hogwatch"

// touchEvery bounds how often a request writes last_seen_at.
const touchEvery = 60 * time.Second

const tokenBytes = 32

// ErrNoSession is returned for every token that does not name a live session
// of an enabled user.
var ErrNoSession = errors.New("session: no such session")

// User is the account behind a session.
type User struct {
	ID          int64
	Username    string
	DisplayName string
	Email       *string
	AdminLocal  bool
	// AdminOIDC is the user's flag from the last OIDC login. It grants nothing
	// by itself: see OIDCAdminSession.
	AdminOIDC bool
	// OIDCAdminSession is true when this session was created by an OIDC login
	// that granted admin, and the user still has admin_oidc. Only such a
	// session carries the OIDC admin right, and it has the short absolute cap.
	OIDCAdminSession bool
	CanListFiles     bool
	HasPassword      bool
	OIDCLinked       bool
	// Notice is the one-time message ("oidc_linked" or "oidc_unlinked") the
	// user has not dismissed yet.
	Notice *string
	// Prefs is the user's display preferences as stored: a JSON object.
	Prefs string
}

// IsAdmin reports whether this session has admin rights: the user is a local
// admin, or the session itself was an OIDC admin login.
func (u User) IsAdmin() bool { return u.AdminLocal || u.OIDCAdminSession }

// Store creates, looks up and revokes sessions.
type Store struct {
	db  *sql.DB
	q   *dbq.Queries
	cfg config.Session
	now func() time.Time
}

// New returns a Store. now is injected so tests control time.
func New(db *sql.DB, cfg config.Session, now func() time.Time) *Store {
	return &Store{db: db, q: dbq.New(db), cfg: cfg, now: now}
}

func hashToken(token string) ([]byte, bool) {
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || len(raw) != tokenBytes {
		return nil, false
	}
	sum := sha256.Sum256(raw)
	return sum[:], true
}

// Create starts a session for userID and returns the cookie value and the
// absolute expiry.
func (s *Store) Create(ctx context.Context, userID int64, oidcAdmin bool, ip, ua string) (string, time.Time, error) {
	raw := make([]byte, tokenBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", time.Time{}, fmt.Errorf("session: token: %w", err)
	}
	sum := sha256.Sum256(raw)
	now := s.now()
	life := s.cfg.AbsoluteTimeout
	var admin int64
	if oidcAdmin {
		life, admin = s.cfg.OIDCAdminTimeout, 1
	}
	expires := now.Add(life)
	err := s.q.InsertSession(ctx, dbq.InsertSessionParams{
		IDHash: sum[:], UserID: userID, CreatedAt: now.Unix(), ExpiresAt: expires.Unix(),
		LastSeenAt: now.Unix(), OidcAdmin: admin, Ip: ip, UserAgent: truncate(ua, 512),
	})
	if err != nil {
		return "", time.Time{}, fmt.Errorf("session: create: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw), expires, nil
}

// Lookup returns the user of a live session. A session that expired, idled
// out, or belongs to a disabled user is deleted and gives ErrNoSession, as
// does any malformed token.
func (s *Store) Lookup(ctx context.Context, token string) (User, error) {
	id, ok := hashToken(token)
	if !ok {
		return User{}, ErrNoSession
	}
	row, err := s.q.GetSessionUser(ctx, id)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return User{}, ErrNoSession
	case err != nil:
		return User{}, fmt.Errorf("session: lookup: %w", err)
	}
	now := s.now()
	idleEnd := time.Unix(row.LastSeenAt, 0).Add(s.cfg.IdleTimeout)
	if !now.Before(time.Unix(row.ExpiresAt, 0)) || !now.Before(idleEnd) || row.Disabled != 0 {
		_ = s.q.DeleteSession(ctx, id)
		return User{}, ErrNoSession
	}
	if now.Sub(time.Unix(row.LastSeenAt, 0)) >= touchEvery {
		if err := s.q.TouchSession(ctx, dbq.TouchSessionParams{LastSeenAt: now.Unix(), IDHash: id}); err != nil {
			return User{}, fmt.Errorf("session: touch: %w", err)
		}
	}
	u := User{
		ID: row.ID, Username: row.Username, DisplayName: row.DisplayName,
		AdminLocal: row.AdminLocal != 0, AdminOIDC: row.AdminOidc != 0, CanListFiles: row.CanListFiles != 0,
		HasPassword: row.HasPassword != 0, OIDCLinked: row.OidcLinked != 0,
		// Both must hold, so a stale session flag cannot outlive a cleared admin_oidc.
		OIDCAdminSession: row.OidcAdmin != 0 && row.AdminOidc != 0,
		Prefs:            row.Prefs,
	}
	if row.Email.Valid {
		u.Email = &row.Email.String
	}
	if row.Notice.Valid {
		u.Notice = &row.Notice.String
	}
	return u, nil
}

// Revoke deletes the session named by token, if any.
func (s *Store) Revoke(ctx context.Context, token string) error {
	id, ok := hashToken(token)
	if !ok {
		return nil
	}
	return s.q.DeleteSession(ctx, id)
}

// RevokeUser deletes every session of the user.
func (s *Store) RevokeUser(ctx context.Context, userID int64) error {
	_, err := s.q.DeleteUserSessions(ctx, userID)
	return err
}

// DeleteExpired removes sessions past their absolute or idle limit.
func (s *Store) DeleteExpired(ctx context.Context) (int64, error) {
	now := s.now()
	return s.q.DeleteExpiredSessions(ctx, dbq.DeleteExpiredSessionsParams{
		Now: now.Unix(), IdleCutoff: now.Add(-s.cfg.IdleTimeout).Unix(),
	})
}

// SetCookie sends the session cookie.
func SetCookie(w http.ResponseWriter, token string, expires time.Time) {
	http.SetCookie(w, &http.Cookie{
		Name: CookieName, Value: token, Path: "/", Expires: expires,
		Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode,
	})
}

// ClearCookie makes the browser drop the session cookie.
func ClearCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name: CookieName, Value: "", Path: "/", MaxAge: -1,
		Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode,
	})
}

// TokenFrom returns the session cookie value. A request with two cookies of
// that name is refused: which one the browser meant is not knowable.
func TokenFrom(r *http.Request) (string, bool) {
	var token string
	var n int
	for _, c := range r.Cookies() {
		if c.Name == CookieName {
			token, n = c.Value, n+1
		}
	}
	return token, n == 1 && token != ""
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}
