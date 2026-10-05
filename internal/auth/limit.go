// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package auth

import (
	"sync"
	"time"
)

// limiter counts failed sign-ins per key (a client address or an e-mail) in a
// sliding window. It lives in memory: a restart forgets it, which only helps the
// person who forgot their password.
type limiter struct {
	mu     sync.Mutex
	max    int
	window time.Duration
	now    func() time.Time
	fails  map[string][]time.Time
}

func newLimiter(maxFails int, window time.Duration) *limiter {
	return &limiter{max: maxFails, window: window, now: time.Now, fails: map[string][]time.Time{}}
}

// blocked reports whether key has used up its failures in the window.
func (l *limiter) blocked(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.recent(key)) >= l.max
}

// recentCount is how many failures key has in the window.
func (l *limiter) recentCount(key string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.recent(key))
}

// fail records a failure for key.
func (l *limiter) fail(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.fails[key] = append(l.recent(key), l.now())
	if len(l.fails) > 10000 { // forget idle keys so the map stays small
		for k := range l.fails {
			if len(l.recent(k)) == 0 {
				delete(l.fails, k)
			}
		}
	}
}

// reset forgets key's failures after a successful sign-in.
func (l *limiter) reset(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.fails, key)
}

func (l *limiter) recent(key string) []time.Time {
	cutoff := l.now().Add(-l.window)
	ts := l.fails[key]
	i := 0
	for i < len(ts) && !ts[i].After(cutoff) {
		i++
	}
	ts = ts[i:]
	if len(ts) == 0 {
		delete(l.fails, key)
		return nil
	}
	l.fails[key] = ts
	return ts
}
