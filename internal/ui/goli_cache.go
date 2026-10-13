// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package ui

import (
	"strings"
	"sync"
	"time"

	"github.com/pipozzz/goliash/internal/goli"
)

// goliCache keeps Goli's answers to first questions on a public demo, where everyone sees the same data: the
// Brief and Explain buttons ask the same questions over and over, and an answer replayed costs nothing. Never
// used elsewhere: there, people with different access may ask the same words.
type goliCache struct {
	mu sync.Mutex
	m  map[string]cachedAnswer
}

type cachedAnswer struct {
	events []goli.Event
	at     time.Time
}

const (
	goliCacheTTL  = 6 * time.Hour
	goliCacheSize = 300
)

func goliCacheKey(workspace, question string) string {
	return workspace + "|" + strings.ToLower(strings.Join(strings.Fields(question), " "))
}

func (c *goliCache) get(key string, now time.Time) ([]goli.Event, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	a, ok := c.m[key]
	if !ok || now.Sub(a.at) > goliCacheTTL {
		return nil, false
	}
	return a.events, true
}

func (c *goliCache) put(key string, events []goli.Event, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m == nil {
		c.m = map[string]cachedAnswer{}
	}
	if len(c.m) >= goliCacheSize {
		oldest, at := "", now
		for k, a := range c.m {
			if a.at.Before(at) {
				oldest, at = k, a.at
			}
		}
		delete(c.m, oldest)
	}
	c.m[key] = cachedAnswer{events: events, at: now}
}
