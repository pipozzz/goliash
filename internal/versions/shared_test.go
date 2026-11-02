// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package versions

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestSharedBuilds(t *testing.T) {
	ctx := context.Background()
	var builds atomic.Int32
	release := make(chan struct{})
	slow := func(context.Context) (int, error) {
		<-release
		return int(builds.Add(1)), nil
	}
	s := sharedBuilds[int]{ttl: time.Minute, cheap: 0, stale: time.Minute, rebuild: 0}

	// Requests that find it missing wait for one build, not one each.
	var wg sync.WaitGroup
	got := make([]int, 8)
	for i := range got {
		wg.Add(1)
		go func() { defer wg.Done(); got[i], _ = s.get(ctx, "k", "g1", slow) }()
	}
	time.Sleep(20 * time.Millisecond)
	close(release)
	wg.Wait()
	for _, v := range got {
		if v != 1 {
			t.Fatalf("built %d times: %v", builds.Load(), got)
		}
	}

	// Stale and expensive: the kept one is served, and built again once in the background.
	if v, _ := s.get(ctx, "k", "g2", slow); v != 1 {
		t.Fatalf("stale not served: %d", v)
	}
	for range 50 {
		if v, _ := s.get(ctx, "k", "g1", slow); v == 2 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if builds.Load() != 2 {
		t.Fatalf("background builds: %d", builds.Load())
	}

	// Cheap: built in the request, so a change shows at once.
	c := sharedBuilds[int]{ttl: time.Minute, cheap: time.Hour, stale: time.Minute}
	n := 0
	cheap := func(context.Context) (int, error) { n++; return n, nil }
	_, _ = c.get(ctx, "k", "g1", cheap)
	if v, _ := c.get(ctx, "k", "g2", cheap); v != 2 {
		t.Fatalf("cheap stale served: %d", v)
	}

	// A request that gives up does not stop the build for the next.
	gone, cancel := context.WithCancel(ctx)
	cancel()
	d := sharedBuilds[int]{ttl: time.Minute, stale: time.Minute}
	if _, err := d.get(gone, "k", "g", func(context.Context) (int, error) { time.Sleep(10 * time.Millisecond); return 7, nil }); err == nil {
		t.Fatal("a cancelled request waited")
	}
	if v, err := d.get(ctx, "k", "g", func(context.Context) (int, error) { return 0, nil }); v != 7 || err != nil {
		t.Fatalf("the build was lost: %d %v", v, err)
	}
}
