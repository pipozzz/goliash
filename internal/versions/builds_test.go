// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package versions

import (
	"testing"
	"time"

	"github.com/pipozzz/goliash/internal/store"
)

func TestNewestBuildOf(t *testing.T) {
	day := func(d int) time.Time { return time.Date(2026, 11, d, 9, 0, 0, 0, time.UTC) }
	times := []store.TagTime{{Tag: "latest", PushedAt: day(9)}, {Tag: "f00d1e5", PushedAt: day(9)}, {Tag: "prod", PushedAt: day(8)}, {Tag: "bca73851", PushedAt: day(3)}}
	b, ok := NewestBuildOf(times, "bca73851")
	if !ok || b.Tag != "f00d1e5" || !b.Newer || !b.RunningPushedAt.Equal(day(3)) {
		t.Fatalf("%+v %v", b, ok)
	}
	if b, _ := NewestBuildOf(times, "f00d1e5"); b.Newer {
		t.Fatalf("runs the newest: %+v", b)
	}
	if _, ok := NewestBuildOf([]store.TagTime{{Tag: "latest", PushedAt: day(9)}}, "latest"); ok {
		t.Fatal("only a moving tag: no build")
	}
}
