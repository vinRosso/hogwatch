package gate_test

import (
	"context"
	"errors"
	"math/rand/v2"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vinRosso/hogwatch/internal/scanner/gate"
	"github.com/vinRosso/hogwatch/internal/scanner/walk"
)

// waitFor polls cond for up to 2 s.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

// acquireAsync starts g.Acquire in a goroutine and reports its completion on
// the returned channel.
func acquireAsync(g walk.Gate) <-chan error {
	ch := make(chan error, 1)
	go func() { ch <- g.Acquire(context.Background()) }()
	return ch
}

func blocked(t *testing.T, ch <-chan error) {
	t.Helper()
	select {
	case err := <-ch:
		t.Fatalf("Acquire returned (%v), want it blocked", err)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestGate_Bound(t *testing.T) {
	const capN = 3
	g := gate.New(capN)
	var maxSeen, held atomic.Int64
	var wg sync.WaitGroup
	for i := range 50 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rng := rand.New(rand.NewPCG(uint64(i), 7)) //nolint:gosec // reproducible test schedule
			for range 200 {
				side := g.Low()
				if rng.IntN(2) == 0 {
					side = g.High()
				}
				if err := side.Acquire(context.Background()); err != nil {
					t.Error(err)
					return
				}
				n := held.Add(1)
				for m := maxSeen.Load(); n > m && !maxSeen.CompareAndSwap(m, n); m = maxSeen.Load() {
				}
				if u := int64(g.InUse()); u > capN {
					t.Errorf("InUse = %d, want <= %d", u, capN)
				}
				if rng.IntN(4) == 0 {
					time.Sleep(time.Microsecond)
				}
				held.Add(-1)
				side.Release()
			}
		}()
	}
	wg.Wait()
	if got := maxSeen.Load(); got != capN {
		t.Fatalf("max tokens held at once = %d, want %d", got, capN)
	}
	if g.InUse() != 0 {
		t.Fatalf("InUse = %d after all released, want 0", g.InUse())
	}
}

func TestGate_HighFirst(t *testing.T) {
	g := gate.New(2)
	ctx := context.Background()
	for range 2 {
		if err := g.Low().Acquire(ctx); err != nil {
			t.Fatal(err)
		}
	}
	lowWaiter := acquireAsync(g.Low())
	blocked(t, lowWaiter)
	highWaiter := acquireAsync(g.High())
	blocked(t, highWaiter)

	g.Low().Release() // one token free: the High waiter gets it
	select {
	case err := <-highWaiter:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the High waiter did not get the released token")
	}
	blocked(t, lowWaiter)
	if g.InUse() != 2 {
		t.Fatalf("InUse = %d, want 2", g.InUse())
	}

	g.High().Release() // now the Low waiter may go
	select {
	case err := <-lowWaiter:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the Low waiter did not get a token once no High waited")
	}
}

func TestGate_LowBlockedWhileHighWaits(t *testing.T) {
	// A free token must not go to a new Low acquire while a High one waits. With
	// a cap of 1 this needs the High waiter queued behind a holder.
	g := gate.New(1)
	ctx := context.Background()
	if err := g.Low().Acquire(ctx); err != nil {
		t.Fatal(err)
	}
	high := acquireAsync(g.High())
	blocked(t, high)
	low := acquireAsync(g.Low())
	blocked(t, low)
	g.Low().Release()
	if err := <-high; err != nil {
		t.Fatal(err)
	}
	blocked(t, low)
	g.High().Release()
	if err := <-low; err != nil {
		t.Fatal(err)
	}
	g.Low().Release()
}

func TestGate_LowNotStarved(t *testing.T) {
	g := gate.New(1)
	var wg sync.WaitGroup
	var done atomic.Int64
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := g.Low().Acquire(context.Background()); err != nil {
				t.Error(err)
				return
			}
			time.Sleep(time.Millisecond)
			g.Low().Release()
			done.Add(1)
		}()
	}
	finished := make(chan struct{})
	go func() { wg.Wait(); close(finished) }()
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatalf("only %d of 4 Low waiters completed", done.Load())
	}
	if g.InUse() != 0 {
		t.Fatalf("InUse = %d, want 0", g.InUse())
	}
}

func TestGate_CancelReleasesNothing(t *testing.T) {
	g := gate.New(1)
	if err := g.Low().Acquire(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, side := range []walk.Gate{g.Low(), g.High()} {
		ctx, cancel := context.WithCancel(context.Background())
		res := make(chan error, 1)
		go func() { res <- side.Acquire(ctx) }()
		time.Sleep(20 * time.Millisecond)
		cancel()
		if err := <-res; !errors.Is(err, context.Canceled) {
			t.Fatalf("Acquire = %v, want context.Canceled", err)
		}
		if g.InUse() != 1 {
			t.Fatalf("InUse = %d after a cancelled Acquire, want 1", g.InUse())
		}
	}
	// A cancelled waiter must not swallow the next release.
	next := acquireAsync(g.Low())
	g.Low().Release()
	if err := <-next; err != nil {
		t.Fatal(err)
	}
	// Already-cancelled context on a free gate: no token taken either.
	g.Low().Release()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := g.High().Acquire(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Acquire on a cancelled context = %v, want context.Canceled", err)
	}
	if g.InUse() != 0 {
		t.Fatalf("InUse = %d, want 0", g.InUse())
	}
}

// Cancel racing with a grant must not leak the token.
func TestGate_CancelRaceNoLeak(t *testing.T) {
	g := gate.New(1)
	for range 500 {
		if err := g.Low().Acquire(context.Background()); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		res := make(chan error, 1)
		go func() { res <- g.High().Acquire(ctx) }()
		go cancel()
		g.Low().Release()
		if err := <-res; err == nil {
			g.High().Release()
		}
		if g.InUse() != 0 {
			t.Fatalf("InUse = %d, want 0 (token leaked)", g.InUse())
		}
	}
	waitFor(t, "idle gate", func() bool { return g.InUse() == 0 })
}

func TestGate_Peak(t *testing.T) {
	g := gate.New(2)
	ctx := context.Background()
	if g.Peak() != 0 {
		t.Fatalf("Peak = %d, want 0", g.Peak())
	}
	_ = g.Low().Acquire(ctx)
	_ = g.High().Acquire(ctx)
	g.Low().Release()
	g.High().Release()
	_ = g.Low().Acquire(ctx)
	g.Low().Release()
	if g.Peak() != 2 {
		t.Fatalf("Peak = %d, want 2", g.Peak())
	}
}
