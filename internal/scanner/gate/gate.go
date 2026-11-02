// Package gate is the scanner's one priority semaphore. Scan workers take
// low-priority tokens while they list a directory and listing jobs take
// high-priority ones, so both together never run more than n directory
// listings at once.
package gate

import (
	"container/list"
	"context"
	"sync"

	"github.com/vinRosso/hogwatch/internal/scanner/walk"
)

// Gate hands out at most n tokens. A freed token goes to the oldest High
// waiter, and to a Low waiter only when no High one waits. A new Low acquire
// never takes a free token while any waiter is queued.
type Gate struct {
	mu    sync.Mutex
	n     int
	inUse int
	peak  int       // the largest inUse ever reached
	high  list.List // of *waiter, oldest first
	low   list.List
}

type waiter struct {
	ready   chan struct{} // closed under mu when the token is granted
	granted bool
	el      *list.Element
}

// New returns a Gate with n tokens (at least 1).
func New(n int) *Gate { return &Gate{n: max(n, 1)} }

// InUse is the number of tokens held right now.
func (g *Gate) InUse() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.inUse
}

// Low returns the low-priority side, which scan workers use.
func (g *Gate) Low() walk.Gate { return side{g: g, high: false} }

// High returns the high-priority side, which listing jobs use.
func (g *Gate) High() walk.Gate { return side{g: g, high: true} }

type side struct {
	g    *Gate
	high bool
}

func (s side) Acquire(ctx context.Context) error { return s.g.acquire(ctx, s.high) }
func (s side) Release()                          { s.g.release() }

func (g *Gate) acquire(ctx context.Context, high bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	g.mu.Lock()
	if g.inUse < g.n && g.high.Len() == 0 && (high || g.low.Len() == 0) {
		g.inUse++
		g.peak = max(g.peak, g.inUse)
		g.mu.Unlock()
		return nil
	}
	w := &waiter{ready: make(chan struct{})}
	if high {
		w.el = g.high.PushBack(w)
	} else {
		w.el = g.low.PushBack(w)
	}
	g.mu.Unlock()

	select {
	case <-w.ready:
		return nil
	case <-ctx.Done():
		g.mu.Lock()
		defer g.mu.Unlock()
		if w.granted {
			// The grant won the race with the cancellation: hand the token on.
			g.inUse--
			g.grant()
		} else if high {
			g.high.Remove(w.el)
		} else {
			g.low.Remove(w.el)
		}
		return ctx.Err()
	}
}

func (g *Gate) release() {
	g.mu.Lock()
	g.inUse--
	g.grant()
	g.mu.Unlock()
}

// grant gives free tokens to waiters, High first. The caller holds mu.
func (g *Gate) grant() {
	for g.inUse < g.n {
		q := &g.high
		if q.Len() == 0 {
			q = &g.low
		}
		front := q.Front()
		if front == nil {
			return
		}
		w := q.Remove(front).(*waiter) //nolint:forcetypeassert // only *waiter is ever pushed
		w.granted = true
		g.inUse++
		g.peak = max(g.peak, g.inUse)
		close(w.ready)
	}
}

// Peak is the largest number of tokens held at once since New.
func (g *Gate) Peak() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.peak
}
