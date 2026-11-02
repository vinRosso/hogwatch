package local

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestLimiter_Backoff(t *testing.T) {
	c := newClock()
	l := NewLimiter(c.now)
	for i := range 5 {
		if _, ok := l.Allow("k"); !ok {
			t.Fatalf("attempt %d refused", i+1)
		}
	}
	for i, want := range []time.Duration{time.Second, 2 * time.Second, 4 * time.Second} {
		ra, ok := l.Allow("k")
		if ok || ra != want {
			t.Fatalf("lockout %d: %v %v, want %v", i, ra, ok, want)
		}
		c.t = c.t.Add(want)
		if _, ok := l.Allow("k"); !ok {
			t.Fatalf("still locked after %v", want)
		}
	}
	for range 30 {
		// Retry exactly when the lockout ends: a longer pause would be idle time.
		if ra, ok := l.Allow("k"); !ok {
			c.t = c.t.Add(ra)
			l.Allow("k")
		}
	}
	if ra, ok := l.Allow("k"); ok || ra != 15*time.Minute {
		t.Fatalf("cap %v %v", ra, ok)
	}
}

func TestLimiter_RefusedCountsForNothing(t *testing.T) {
	c := newClock()
	l := NewLimiter(c.now)
	for range 5 {
		l.Allow("k")
	}
	for range 50 {
		if _, ok := l.Allow("k"); ok {
			t.Fatal("admitted while locked")
		}
	}
	c.t = c.t.Add(time.Second)
	if _, ok := l.Allow("k"); !ok {
		t.Fatal("hammering a locked key extended the lock")
	}
}

func TestLimiter_ParallelBurst(t *testing.T) {
	l := NewLimiter(newClock().now)
	var admitted atomic.Int32
	var wg sync.WaitGroup
	for range 200 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, ok := l.Allow("burst"); ok {
				admitted.Add(1)
			}
		}()
	}
	wg.Wait()
	if n := admitted.Load(); n != 5 {
		t.Fatalf("%d of 200 parallel attempts admitted, want 5", n)
	}
}

func TestLimiter_SuccessResets(t *testing.T) {
	l := NewLimiter(newClock().now)
	for range 5 {
		l.Allow("ip", "user")
	}
	if _, ok := l.Allow("ip", "user"); ok {
		t.Fatal("not locked")
	}
	l.Succeed("ip", "user")
	if _, ok := l.Allow("ip", "user"); !ok {
		t.Fatal("still locked")
	}
}

func TestLimiter_SharedKey(t *testing.T) {
	l := NewLimiter(newClock().now)
	for range 5 {
		l.Allow("user:ghost") // no such user: the limiter does not care
	}
	if _, ok := l.Allow("ip:2", "user:ghost"); ok {
		t.Fatal("unknown username not locked")
	}
	if _, ok := l.Allow("ip:2", "user:other"); !ok {
		t.Fatal("unrelated keys locked")
	}
}

func TestLimiter_Evicts(t *testing.T) {
	l := NewLimiter(newClock().now)
	for range 5 {
		l.Allow("victim") // locked
	}
	for i := range maxKeys + 10 {
		l.Allow(fmt.Sprintf("k%d", i))
		if i%1000 == 0 {
			l.Allow("victim") // keeps the victim recently used
		}
	}
	if len(l.keys) != maxKeys {
		t.Fatalf("%d keys", len(l.keys))
	}
	if _, found := l.keys["k0"]; found {
		t.Fatal("oldest key kept")
	}
	if _, found := l.keys[fmt.Sprintf("k%d", maxKeys+9)]; !found {
		t.Fatal("newest key dropped")
	}
	if _, found := l.keys["victim"]; !found {
		t.Fatal("a recently used key was evicted")
	}
}

func TestLimiter_SucceedLeavesOtherKeys(t *testing.T) {
	l := NewLimiter(newClock().now)
	for range 5 {
		l.Allow("ip", "user:a")
	}
	l.Succeed("user:a")
	if _, ok := l.Allow("ip"); ok {
		t.Fatal("Succeed on one key unlocked another")
	}
	if _, ok := l.Allow("user:a"); !ok {
		t.Fatal("the succeeded key stayed locked")
	}
}

func TestLimiter_FloodKeepsLockedKey(t *testing.T) {
	l := NewLimiter(newClock().now)
	for range 5 {
		l.Allow("victim")
	}
	// The locked key is the oldest entry; a flood of fresh keys must not push it out.
	for i := range maxKeys * 2 {
		l.Allow(fmt.Sprintf("flood%d", i))
	}
	if _, ok := l.Allow("victim"); ok {
		t.Fatal("a flood evicted a locked key")
	}
}

func TestLimiter_IdleKeyRestarts(t *testing.T) {
	c := newClock()
	l := NewLimiter(c.now)
	// 20 failures spread over days must not leave the key one failure from a lockout.
	for range 20 {
		if _, ok := l.Allow("ip"); !ok {
			t.Fatal("a slow trickle was refused")
		}
		c.t = c.t.Add(maxLockout + time.Second)
	}
	for i := range freeAttempts - 1 {
		if _, ok := l.Allow("ip"); !ok {
			t.Fatalf("attempt %d after the idle period refused", i+1)
		}
	}
	if _, ok := l.Allow("ip"); !ok {
		t.Fatal("the 5th attempt is still admitted; it only sets the first lock")
	}
	if ra, ok := l.Allow("ip"); ok || ra != baseLockout {
		t.Fatalf("restart did not escalate from the base lockout: %v %v", ra, ok)
	}
}

func TestLimiter_ActiveKeyKeepsEscalating(t *testing.T) {
	c := newClock()
	l := NewLimiter(c.now)
	for range freeAttempts {
		l.Allow("k")
	}
	// Retrying exactly at each lockout's end forgets at most one attempt per cap.
	var last time.Duration
	for range 30 {
		ra, ok := l.Allow("k")
		if ok {
			t.Fatal("admitted while locked")
		}
		c.t = c.t.Add(ra)
		if _, ok := l.Allow("k"); !ok {
			t.Fatal("refused after the lockout ended")
		}
		last = ra
	}
	if last != maxLockout {
		t.Fatalf("lockout %v, want the %v cap", last, maxLockout)
	}
}

func TestLimiter_CappedKeyStaysCappedJustPastLockout(t *testing.T) {
	c := newClock()
	l := NewLimiter(c.now)
	for range freeAttempts + 20 {
		if ra, ok := l.Allow("k"); !ok {
			c.t = c.t.Add(ra)
			l.Allow("k")
		}
	}
	// A sprayer retrying a second after each capped lockout must not win back
	// free attempts: the decay forgets at most one attempt per maxLockout.
	for range 10 {
		ra, ok := l.Allow("k")
		if ok || ra <= 0 {
			t.Fatalf("admitted while locked: %v %v", ra, ok)
		}
		c.t = c.t.Add(ra + time.Second)
		if _, ok := l.Allow("k"); !ok {
			t.Fatal("refused after the lockout ended")
		}
		if ra, ok := l.Allow("k"); ok || ra != maxLockout {
			t.Fatalf("lockout after a capped retry %v %v, want the %v cap", ra, ok, maxLockout)
		}
	}
}
