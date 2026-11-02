package oidc

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/vinRosso/hogwatch/internal/auth/oidc/oidctest"
)

func TestIdentity_LongEmailIsAbsent(t *testing.T) {
	long := strings.Repeat("a", MaxEmail) + "@x.example" // over the limit
	id := identityFrom("i", "s", map[string]any{"email": long, "email_verified": true}, "groups", "")
	if id.Email != "" || id.EmailVerified {
		t.Fatalf("over-long email kept: len %d verified %v", len(id.Email), id.EmailVerified)
	}
	atLimit := strings.Repeat("a", MaxEmail-len("@x.example")) + "@x.example"
	if got := identityFrom("i", "s", map[string]any{"email": atLimit}, "groups", "").Email; got != atLimit {
		t.Fatalf("an email at the limit was dropped (len %d)", len(got))
	}
}

func TestClient_SubjectTooLong(t *testing.T) {
	for name, tc := range map[string]struct {
		sub     string
		wantErr bool
	}{
		"at the limit": {strings.Repeat("s", MaxSubject), false},
		"over":         {strings.Repeat("s", MaxSubject+1), true},
	} {
		t.Run(name, func(t *testing.T) {
			iss := oidctest.New(t)
			iss.Next(map[string]any{"sub": tc.sub})
			c := newClient(t, iss)
			f := begin(t, c, iss)
			_, err := c.Exchange(context.Background(), f.code, f.verifier, f.nonce)
			if tc.wantErr != errors.Is(err, ErrFailed) {
				t.Fatalf("err = %v, want failure %v", err, tc.wantErr)
			}
		})
	}
}
