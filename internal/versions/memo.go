// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package versions

import (
	"context"
	"sync"
)

// One page builds the overview and the vulnerability report from several places (the posture, the base images,
// the evidence …). A request's context can carry a memo, so each is built once per request; what it returns is
// shared within the request and must not be changed in place.

type memoKey struct{}

type memo struct {
	mu sync.Mutex
	m  map[string]any
}

// WithMemo returns a context in which LoadOverview and LoadVulnReport are built once.
func WithMemo(ctx context.Context) context.Context {
	if _, ok := ctx.Value(memoKey{}).(*memo); ok {
		return ctx
	}
	return context.WithValue(ctx, memoKey{}, &memo{m: map[string]any{}})
}

// memoized returns what load returned for key earlier in the request, or loads it.
func memoized[T any](ctx context.Context, key string, load func() (T, error)) (T, error) {
	m, ok := ctx.Value(memoKey{}).(*memo)
	if !ok {
		return load()
	}
	m.mu.Lock()
	if v, ok := m.m[key]; ok {
		m.mu.Unlock()
		return v.(T), nil
	}
	m.mu.Unlock()
	v, err := load()
	if err == nil {
		m.mu.Lock()
		m.m[key] = v
		m.mu.Unlock()
	}
	return v, err
}
