package local

import (
	"container/list"
	"sync"
	"time"
)

const (
	freeAttempts = 5
	baseLockout  = time.Second
	maxLockout   = 15 * time.Minute
	maxKeys      = 100_000
	evictScan    = 64
)

type entry struct {
	key         string
	attempts    int
	lockedUntil time.Time
	lastAttempt time.Time // the last admitted attempt, for the idle decay
}

// Limiter rate-limits login attempts per key (the client IP and the
// lowercased username). It never looks users up, so unknown names lock like
// real ones.
//
// Allow counts the attempt when it admits it, before the password is checked,
// so a burst of parallel requests cannot all slip in ahead of the first
// failure. A success calls Succeed, which forgets the keys.
type Limiter struct {
	now func() time.Time

	mu    sync.Mutex
	keys  map[string]*list.Element // of *entry; front is the least recently used
	order *list.List
}

// NewLimiter returns an empty limiter.
func NewLimiter(now func() time.Time) *Limiter {
	return &Limiter{now: now, keys: map[string]*list.Element{}, order: list.New()}
}

// Allow admits an attempt on all keys, or refuses it. After 5 admitted
// attempts without a Succeed, a key refuses for 1 s × 2^(n−5) (n = attempts
// so far), capped at 15 minutes. A refused call counts for nothing and
// returns the longest remaining lockout over the keys. Each full 15 minutes
// since a key's last admitted attempt forgets one of its attempts, so failures
// spread over days never add up, while a key retried at the end of every
// capped lockout stays at the cap.
func (l *Limiter) Allow(keys ...string) (retryAfter time.Duration, ok bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	for _, k := range keys {
		if el, found := l.keys[k]; found {
			if d := el.Value.(*entry).lockedUntil.Sub(now); d > retryAfter {
				retryAfter = d
			}
		}
	}
	if retryAfter > 0 {
		return retryAfter, false
	}
	for _, k := range keys {
		e := l.entry(k)
		// One attempt is forgotten per maxLockout of quiet. A key retried at
		// every capped lockout's end stays capped; scattered failures drain.
		if e.attempts > 0 {
			e.attempts = max(0, e.attempts-int(now.Sub(e.lastAttempt)/maxLockout))
		}
		e.attempts++
		e.lastAttempt = now
		if e.attempts >= freeAttempts {
			d := maxLockout
			if shift := e.attempts - freeAttempts; shift < 20 {
				d = min(baseLockout<<shift, maxLockout)
			}
			e.lockedUntil = now.Add(d)
		}
	}
	return 0, true
}

// Succeed forgets the keys.
func (l *Limiter) Succeed(keys ...string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, k := range keys {
		if el, found := l.keys[k]; found {
			l.order.Remove(el)
			delete(l.keys, k)
		}
	}
}

// entry returns the entry of k, creating it and marking it most recently
// used. At the key limit the least recently used key is dropped.
func (l *Limiter) entry(k string) *entry {
	if el, found := l.keys[k]; found {
		l.order.MoveToBack(el)
		return el.Value.(*entry)
	}
	for len(l.keys) >= maxKeys {
		// Drop the oldest key that is not locked, looking at a bounded number, so
		// a flood of fresh keys cannot push a lockout out. If every candidate is
		// locked, the oldest goes.
		victim, now := l.order.Front(), l.now()
		for el, i := victim, 0; el != nil && i < evictScan; el, i = el.Next(), i+1 {
			if !el.Value.(*entry).lockedUntil.After(now) {
				victim = el
				break
			}
		}
		l.order.Remove(victim)
		delete(l.keys, victim.Value.(*entry).key)
	}
	e := &entry{key: k}
	l.keys[k] = l.order.PushBack(e)
	return e
}
