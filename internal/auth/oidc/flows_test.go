package oidc

import (
	"crypto/rand"
	"testing"
	"time"
)

type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

func newFlows() (*Flows, *clock) {
	c := &clock{t: time.Unix(1_700_000_000, 0)}
	return NewFlows(c.now, rand.Reader), c
}

func TestFlows_OneTime(t *testing.T) {
	f, _ := newFlows()
	fl, err := f.Begin(ModeLink, 42)
	if err != nil {
		t.Fatal(err)
	}
	if len(fl.State) < 43 || len(fl.Nonce) < 43 || len(fl.Verifier) < 43 {
		t.Fatalf("short secrets: %d %d %d", len(fl.State), len(fl.Nonce), len(fl.Verifier))
	}
	if fl.State == fl.Nonce || fl.Nonce == fl.Verifier {
		t.Fatal("state, nonce and verifier must be independent")
	}
	got, ok := f.Take(fl.State)
	if !ok || got.Mode != ModeLink || got.LinkUserID != 42 || got.Verifier != fl.Verifier || got.Nonce != fl.Nonce {
		t.Fatalf("Take = %+v, %v", got, ok)
	}
	if _, ok := f.Take(fl.State); ok {
		t.Fatal("a second Take succeeded")
	}
	if _, ok := f.Take("unknown"); ok {
		t.Fatal("unknown state succeeded")
	}
}

func TestFlows_Expiry(t *testing.T) {
	f, c := newFlows()
	a, _ := f.Begin(ModeLogin, 0)
	b, _ := f.Begin(ModeLogin, 0)
	c.t = c.t.Add(FlowTTL - time.Nanosecond)
	if _, ok := f.Take(a.State); !ok {
		t.Fatal("flow just inside the TTL was refused")
	}
	c.t = c.t.Add(time.Nanosecond) // exactly the TTL
	if _, ok := f.Take(b.State); ok {
		t.Fatal("flow at exactly the TTL was accepted")
	}
}

func TestFlows_Cap(t *testing.T) {
	f, c := newFlows()
	first, _ := f.Begin(ModeLogin, 0)
	var last Flow
	for i := 0; i < MaxFlows; i++ {
		c.t = c.t.Add(time.Millisecond)
		last, _ = f.Begin(ModeLogin, 0)
	}
	if n := f.Len(); n != MaxFlows {
		t.Fatalf("Len = %d, want %d", n, MaxFlows)
	}
	if _, ok := f.Take(first.State); ok {
		t.Fatal("the oldest flow survived the cap")
	}
	if _, ok := f.Take(last.State); !ok {
		t.Fatal("the newest flow was evicted")
	}
}

func TestFlows_Sweep(t *testing.T) {
	f, c := newFlows()
	_, _ = f.Begin(ModeLogin, 0)
	c.t = c.t.Add(FlowTTL)
	fresh, _ := f.Begin(ModeLogin, 0)
	f.Sweep()
	if n := f.Len(); n != 1 {
		t.Fatalf("Len after Sweep = %d, want 1", n)
	}
	if _, ok := f.Take(fresh.State); !ok {
		t.Fatal("Sweep removed a live flow")
	}
}

func TestFlows_PerClientCap(t *testing.T) {
	f := NewFlows(time.Now, rand.Reader)
	other, err := f.BeginFrom("198.51.100.7", ModeLogin, 0)
	if err != nil {
		t.Fatal(err)
	}
	var first Flow
	for i := 0; i < 3*MaxFlowsPerClient; i++ {
		fl, err := f.BeginFrom("203.0.113.9", ModeLogin, 0)
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			first = fl
		}
	}
	if got := f.Len(); got != MaxFlowsPerClient+1 {
		t.Fatalf("pending = %d, want %d (one client capped, the other untouched)", got, MaxFlowsPerClient+1)
	}
	if _, ok := f.Take(first.State); ok {
		t.Fatal("the flooding client's oldest flow survived")
	}
	if _, ok := f.Take(other.State); !ok {
		t.Fatal("another client's flow was evicted")
	}
}
