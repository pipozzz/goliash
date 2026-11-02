// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package versions

import (
	"context"
	"sync"
	"time"
)

// The overview and the vulnerability report are read by every page, the badges and the notifier, and building
// either goes through the whole workspace. They are kept while the tables they are read from are unchanged. While
// images are being read, those tables are written every few seconds: a report that takes long to build is then not
// built by each request that finds it stale, but once, in the background, while requests get the one kept, for at
// most stale. A cheap one (a small workspace) is built in the request, so a page shows a change at once. Requests
// that find it being built wait for that build instead of building it too.

// sharedBuilds keeps what load built, by key.
type sharedBuilds[T any] struct {
	ttl     time.Duration // how long one is reused while its tables are unchanged (other processes' writes)
	cheap   time.Duration // one built faster than this is built again in the request
	stale   time.Duration // the oldest one served while a newer one is built
	rebuild time.Duration // the least time between two background builds

	mu       sync.Mutex
	kept     map[string]kept[T]
	building map[string]*building[T]
}

type kept[T any] struct {
	gens string
	at   time.Time
	took time.Duration
	v    T
}

type building[T any] struct {
	done chan struct{}
	v    T
	err  error
}

// buildTimeout bounds one build, which goes on when the request that started it gives up.
const buildTimeout = 5 * time.Minute

// get returns what is kept for key when gens (the generations of its tables) are those it was built at, else
// builds it as described above. Callers must not change what it returns in place.
func (s *sharedBuilds[T]) get(ctx context.Context, key, gens string, load func(context.Context) (T, error)) (T, error) {
	s.mu.Lock()
	k, ok := s.kept[key]
	b := s.building[key]
	switch {
	case ok && k.gens == gens && time.Since(k.at) < s.ttl:
		s.mu.Unlock()
		return k.v, nil
	case ok && k.took >= s.cheap && time.Since(k.at) < s.stale:
		if b == nil && time.Since(k.at) >= s.rebuild {
			s.start(ctx, key, gens, load)
		}
		s.mu.Unlock()
		return k.v, nil
	case b == nil:
		b = s.start(ctx, key, gens, load)
	}
	s.mu.Unlock()
	select {
	case <-b.done:
		return b.v, b.err
	case <-ctx.Done():
		var zero T
		return zero, ctx.Err()
	}
}

// start builds in a goroutine; s.mu is held by the caller. What is built is kept with the generations read before
// it was built, so a write during the build makes it stale, not lost.
func (s *sharedBuilds[T]) start(ctx context.Context, key, gens string, load func(context.Context) (T, error)) *building[T] {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), buildTimeout)
	b := &building[T]{done: make(chan struct{})}
	if s.building == nil {
		s.building, s.kept = map[string]*building[T]{}, map[string]kept[T]{}
	}
	s.building[key] = b
	go func() {
		defer cancel()
		started := time.Now()
		b.v, b.err = load(ctx)
		s.mu.Lock()
		delete(s.building, key)
		if b.err == nil {
			s.kept[key] = kept[T]{gens: gens, at: time.Now(), took: time.Since(started), v: b.v}
		}
		s.mu.Unlock()
		close(b.done)
	}()
	return b
}
