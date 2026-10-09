// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package versions

import (
	"testing"
	"time"

	"github.com/pipozzz/goliash/internal/store"
)

func TestBehindSince(t *testing.T) {
	day := func(d int) time.Time { return time.Date(2026, 9, d, 0, 0, 0, 0, time.UTC) }
	fallback := day(30)
	rels := []store.Release{
		{Version: "1.4.0", PublishedAt: day(1)},
		{Version: "1.5.0", PublishedAt: day(10)},
		{Version: "1.5.1", DiscoveredAt: day(5)}, // date unknown: seen earlier than 1.5.0 came out
		{Version: "1.6.0-rc.1", PublishedAt: day(2)},
		{Version: "1.6.0", PublishedAt: day(20)},
	}
	if at, known := behindSince("1.4.0", rels, fallback); !at.Equal(day(5)) || known {
		t.Errorf("1.4.0: %v %v, want the first sighting as a lower bound", at, known)
	}
	if at, known := behindSince("1.5.1", rels, fallback); !at.Equal(day(20)) || !known {
		t.Errorf("1.5.1: %v %v", at, known)
	}
	if at, known := behindSince("1.6.0", rels, fallback); !at.Equal(fallback) || known {
		t.Errorf("newest: %v %v", at, known)
	}
	if at, _ := behindSince("latest", rels, fallback); !at.Equal(fallback) {
		t.Errorf("unparsed: %v", at)
	}
}

func TestPercentile(t *testing.T) {
	if got := percentile(nil, 50); got != 0 {
		t.Errorf("empty %d", got)
	}
	v := []int{40, 1, 3, 90, 7}
	if got := percentile(v, 50); got != 7 {
		t.Errorf("median %d", got)
	}
	if got := percentile(v, 90); got != 90 {
		t.Errorf("p90 %d", got)
	}
}
