package local

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"io"
	"sync"
	"time"
)

// SetupTokenTTL is how long a setup token stays valid.
const SetupTokenTTL = time.Hour

// SetupToken is the one-time secret that authorises creating the first admin.
// It lives only in memory.
type SetupToken struct {
	now func() time.Time
	rnd io.Reader

	mu      sync.Mutex
	token   string
	expires time.Time
}

// NewSetupToken returns a SetupToken holding a fresh token. A nil rnd means
// crypto/rand.
func NewSetupToken(now func() time.Time, rnd io.Reader) *SetupToken {
	if rnd == nil {
		rnd = rand.Reader
	}
	s := &SetupToken{now: now, rnd: rnd}
	s.Rotate()
	return s
}

// Current returns the token and its expiry; the token is empty after Invalidate.
func (s *SetupToken) Current() (token string, expires time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.token, s.expires
}

// Rotate replaces the token with a new one valid for SetupTokenTTL.
func (s *SetupToken) Rotate() string {
	b := make([]byte, 32)
	if _, err := io.ReadFull(s.rnd, b); err != nil {
		// A failing entropy source must not leave a guessable token.
		panic("local: setup token: " + err.Error())
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.token = base64.RawURLEncoding.EncodeToString(b)
	s.expires = s.now().Add(SetupTokenTTL)
	return s.token
}

// Check reports whether t is the current, unexpired token.
func (s *SetupToken) Check(t string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.token == "" || !s.now().Before(s.expires) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(t), []byte(s.token)) == 1
}

// Invalidate discards the token until the next Rotate.
func (s *SetupToken) Invalidate() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.token = ""
}
