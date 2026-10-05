// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package auth

import (
	"context"
	"time"

	"github.com/pipozzz/goliash/internal/store"
)

// limiter counts failed sign-ins per key (a client address, an e-mail, a person) in
// a sliding window. Counts live in the database, so every server of a cluster sees
// the same ones; keys are stored as hashes. Errors count as "not blocked": a broken
// database must not lock everyone out, and signing in fails then anyway.
type limiter struct {
	store  *store.Store
	scope  string // keeps the limiters' keys apart
	max    int
	window time.Duration
	now    func() time.Time
}

func newLimiter(st *store.Store, scope string, maxFails int, window time.Duration) *limiter {
	return &limiter{store: st, scope: scope, max: maxFails, window: window, now: time.Now}
}

func (l *limiter) key(k string) string { return hashOf(l.scope + "\x00" + k) }

// blocked reports whether key has used up its failures in the window.
func (l *limiter) blocked(ctx context.Context, key string) bool {
	return l.recentCount(ctx, key) >= l.max
}

// recentCount is how many failures key has in the window.
func (l *limiter) recentCount(ctx context.Context, key string) int {
	n, err := l.store.SigninFailures(ctx, l.key(key), l.now().Add(-l.window))
	if err != nil {
		return 0
	}
	return n
}

// fail records a failure for key.
func (l *limiter) fail(ctx context.Context, key string) {
	_ = l.store.AddSigninFailure(ctx, l.key(key))
}

// reset forgets key's failures after a successful sign-in.
func (l *limiter) reset(ctx context.Context, key string) {
	_ = l.store.ClearSigninFailures(ctx, l.key(key))
}
