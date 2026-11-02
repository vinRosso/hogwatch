// Package local holds the primitives of local-account authentication:
// password hashing, the login limiter and the one-time setup token.
package local

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"

	"golang.org/x/crypto/argon2"
)

// Argon2id parameters of new hashes.
const (
	memoryKiB = 19456
	timeCost  = 2
	threads   = 1
	saltLen   = 16
	keyLen    = 32

	// Verify refuses hashes whose parameters would let a crafted value
	// exhaust memory or CPU.
	maxMemoryKiB = 1 << 20
	maxTime      = 10
	maxThreads   = 16

	// MaxPasswordBytes is the longest password accepted anywhere.
	MaxPasswordBytes = 1024

	maxConcurrent = 4
)

// ErrMalformed is returned by Verify for a value that is not a usable PHC string.
var ErrMalformed = errors.New("local: malformed password hash")

var (
	sem = make(chan struct{}, maxConcurrent)

	active atomic.Int32
	// onHash is a test seam called with the number of hashes running.
	onHash func(active int32)

	// DummyVerifies counts DummyVerify calls; tests assert that a request
	// for an unknown user still burns one hash.
	DummyVerifies atomic.Int64

	dummyOnce sync.Once
	dummyHash string
)

func acquire() func() {
	sem <- struct{}{}
	n := active.Add(1)
	if onHash != nil {
		onHash(n)
	}
	return func() {
		active.Add(-1)
		<-sem
	}
}

func derive(pw string, salt []byte, m, t uint32, p uint8, n uint32) []byte {
	defer acquire()()
	return argon2.IDKey([]byte(pw), salt, t, m, p, n)
}

// Hash returns the PHC string of pw.
func Hash(pw string) (string, error) {
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("local: salt: %w", err)
	}
	key := derive(pw, salt, memoryKiB, timeCost, threads, keyLen)
	b := base64.RawStdEncoding
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, memoryKiB, timeCost, threads, b.EncodeToString(salt), b.EncodeToString(key)), nil
}

// Verify reports whether pw matches the PHC string phc. It reads the cost
// parameters from phc, so hashes made with other parameters still verify.
func Verify(phc, pw string) (bool, error) {
	parts := strings.Split(phc, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" {
		return false, ErrMalformed
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return false, ErrMalformed
	}
	var m, t uint32
	var p uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &m, &t, &p); err != nil {
		return false, ErrMalformed
	}
	if m == 0 || m > maxMemoryKiB || t == 0 || t > maxTime || p == 0 || p > maxThreads {
		return false, ErrMalformed
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil || len(salt) == 0 {
		return false, ErrMalformed
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil || len(want) == 0 {
		return false, ErrMalformed
	}
	got := derive(pw, salt, m, t, p, uint32(len(want))) //nolint:gosec // len(want) is bounded by the PHC string, itself bounded by the 64 KiB body cap
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}

// DummyVerify burns one hash so a login for an unknown or password-less user
// takes as long as a real one.
func DummyVerify(pw string) {
	DummyVerifies.Add(1)
	dummyOnce.Do(func() { dummyHash, _ = Hash("hogwatch-dummy") })
	_, _ = Verify(dummyHash, pw)
}
