package local

import (
	"encoding/base64"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/argon2"
)

type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

func newClock() *clock { return &clock{t: time.Unix(1_700_000_000, 0)} }

func b64(b []byte) string { return base64.RawStdEncoding.EncodeToString(b) }

func TestHash_Format(t *testing.T) {
	h, err := Hash("correct horse")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(h, "$argon2id$v=19$m=19456,t=2,p=1$") {
		t.Fatalf("hash %q", h)
	}
}

func TestVerify(t *testing.T) {
	h, _ := Hash("secret-pw")
	if ok, err := Verify(h, "secret-pw"); !ok || err != nil {
		t.Fatalf("right password: %v %v", ok, err)
	}
	if ok, err := Verify(h, "wrong"); ok || err != nil {
		t.Fatalf("wrong password: %v %v", ok, err)
	}
	// Other parameters still verify.
	salt := []byte("0123456789abcdef")
	key := argon2.IDKey([]byte("pw"), salt, 1, 8192, 2, 32)
	other := fmt.Sprintf("$argon2id$v=19$m=8192,t=1,p=2$%s$%s", b64(salt), b64(key))
	if ok, err := Verify(other, "pw"); !ok || err != nil {
		t.Fatalf("other params: %v %v", ok, err)
	}
}

func TestVerify_Malformed(t *testing.T) {
	for _, h := range []string{
		"", "x", "$argon2id$v=19$m=1,t=1,p=1$a", "$bcrypt$v=19$m=1,t=1,p=1$YQ$YQ",
		"$argon2id$v=19$m=99999999,t=1,p=1$YQ$YQ", "$argon2id$v=19$m=8,t=0,p=1$YQ$YQ",
		"$argon2id$v=19$m=8,t=1,p=1$!!$YQ",
	} {
		if ok, err := Verify(h, "pw"); ok || err == nil {
			t.Errorf("Verify(%q) = %v, %v", h, ok, err)
		}
	}
}

func TestHash_Concurrency(t *testing.T) {
	var peak atomic.Int32
	onHash = func(n int32) {
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				return
			}
		}
	}
	defer func() { onHash = nil }()
	var wg sync.WaitGroup
	for range 16 {
		wg.Add(1)
		go func() { defer wg.Done(); _, _ = Hash("pw") }()
	}
	wg.Wait()
	if p := peak.Load(); p > 4 || p < 1 {
		t.Fatalf("peak concurrency %d", p)
	}
}

func TestSetupToken_Expiry(t *testing.T) {
	c := newClock()
	s := NewSetupToken(c.now, nil)
	tok, exp := s.Current()
	if !exp.Equal(c.t.Add(time.Hour)) || tok == "" {
		t.Fatalf("token %q expires %v", tok, exp)
	}
	c.t = c.t.Add(59*time.Minute + 59*time.Second)
	if !s.Check(tok) {
		t.Fatal("invalid at 59:59")
	}
	if s.Check(tok + "x") {
		t.Fatal("wrong token accepted")
	}
	c.t = c.t.Add(time.Second)
	if s.Check(tok) {
		t.Fatal("valid at 1h")
	}
	next := s.Rotate()
	if next == tok || !s.Check(next) || s.Check(tok) {
		t.Fatal("rotate")
	}
	s.Invalidate()
	if s.Check(next) {
		t.Fatal("valid after Invalidate")
	}
}
