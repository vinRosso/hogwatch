package oidc

import (
	"encoding/base64"
	"io"
	"sync"
	"time"
)

const (
	// FlowTTL is how long a started login may take at the identity provider.
	FlowTTL = 10 * time.Minute
	// MaxFlows bounds the pending flows so unauthenticated /oidc/start calls
	// cannot grow memory; the oldest is evicted first.
	MaxFlows = 10_000
	// MaxFlowsPerClient bounds the pending flows one client address may hold;
	// its own oldest is evicted first, so a single address flooding /oidc/start
	// cannot push other people's logins out of the store.
	MaxFlowsPerClient = 20
)

// Mode says what the callback does once the identity is verified.
type Mode int

const (
	ModeLogin Mode = iota // log in, linking or creating an account by the rules
	ModeLink              // link the identity to LinkUserID
)

// Flow is one pending authorization: the secrets that bind the callback to
// the browser that started it.
type Flow struct {
	State, Nonce, Verifier string
	Mode                   Mode
	LinkUserID             int64
	created                time.Time
	client                 string
}

// Flows is the in-memory store of pending flows. A restart drops them; the
// user just starts again.
type Flows struct {
	now func() time.Time
	rnd io.Reader

	mu    sync.Mutex
	m     map[string]Flow
	order []string       // states in creation order; entries may be stale
	byCli map[string]int // live flows per non-empty client
}

// NewFlows builds a store with an injectable clock and entropy source.
func NewFlows(now func() time.Time, rnd io.Reader) *Flows {
	return &Flows{now: now, rnd: rnd, m: map[string]Flow{}, byCli: map[string]int{}}
}

func (f *Flows) secret() (string, error) {
	b := make([]byte, 32)
	if _, err := io.ReadFull(f.rnd, b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// Begin registers a new flow with fresh, independent state, nonce and PKCE
// verifier (each 32 random bytes, 43 base64url characters).
func (f *Flows) Begin(mode Mode, linkUserID int64) (Flow, error) {
	return f.BeginFrom("", mode, linkUserID)
}

// BeginFrom is Begin for a request from client (an address; empty means
// unknown and is not limited per client).
func (f *Flows) BeginFrom(client string, mode Mode, linkUserID int64) (Flow, error) {
	fl := Flow{Mode: mode, LinkUserID: linkUserID, client: client}
	for _, dst := range []*string{&fl.State, &fl.Nonce, &fl.Verifier} {
		s, err := f.secret()
		if err != nil {
			return Flow{}, err
		}
		*dst = s
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	fl.created = f.now()
	for client != "" && f.byCli[client] >= MaxFlowsPerClient && f.evictOldestOf(client) {
	}
	for len(f.m) >= MaxFlows && len(f.order) > 0 {
		f.remove(f.order[0])
		f.order = f.order[1:]
	}
	if len(f.order) > 2*MaxFlows {
		f.compact()
	}
	f.m[fl.State] = fl
	f.order = append(f.order, fl.State)
	if client != "" {
		f.byCli[client]++
	}
	return fl, nil
}

// remove deletes a live flow and its per-client count; the caller holds mu.
func (f *Flows) remove(state string) {
	fl, ok := f.m[state]
	if !ok {
		return
	}
	delete(f.m, state)
	if fl.client != "" {
		if f.byCli[fl.client]--; f.byCli[fl.client] <= 0 {
			delete(f.byCli, fl.client)
		}
	}
}

// evictOldestOf drops the oldest live flow of client and reports whether it
// found one; the caller holds mu.
func (f *Flows) evictOldestOf(client string) bool {
	for _, s := range f.order {
		if fl, ok := f.m[s]; ok && fl.client == client {
			f.remove(s)
			return true
		}
	}
	return false
}

// Take removes and returns the flow of a state. It fails for an unknown,
// already used or expired state, so a callback cannot be replayed.
func (f *Flows) Take(state string) (Flow, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fl, ok := f.m[state]
	if !ok {
		return Flow{}, false
	}
	f.remove(state)
	if f.now().Sub(fl.created) >= FlowTTL {
		return Flow{}, false
	}
	return fl, true
}

// Sweep drops expired flows.
func (f *Flows) Sweep() {
	f.mu.Lock()
	defer f.mu.Unlock()
	now := f.now()
	for s, fl := range f.m {
		if now.Sub(fl.created) >= FlowTTL {
			f.remove(s)
		}
	}
	f.compact()
}

// Len is the number of pending flows.
func (f *Flows) Len() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.m)
}

// compact rebuilds order from the live flows; the caller holds mu.
func (f *Flows) compact() {
	live := f.order[:0:0]
	for _, s := range f.order {
		if _, ok := f.m[s]; ok {
			live = append(live, s)
		}
	}
	f.order = live
}
