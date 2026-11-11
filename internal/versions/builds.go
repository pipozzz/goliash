// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package versions

import (
	"strings"
	"time"

	"github.com/pipozzz/goliash/internal/store"
)

// NewestBuild is, for a service running a tag that is no version (a commit hash), the newest build its registry
// has, by push time, and when the one running was pushed: what orders builds no version orders.
type NewestBuild struct {
	Tag             string
	PushedAt        time.Time
	RunningPushedAt time.Time // zero when the running tag is not among those known
	Newer           bool      // the newest is not what runs
}

// NewestBuildOf picks the newest build among a repository's tags (newest pushed first), leaving out moving
// tags such as latest or main, and compares it with the running tag. ok is false when none is known.
func NewestBuildOf(times []store.TagTime, running string) (NewestBuild, bool) {
	var b NewestBuild
	found := false
	for _, t := range times {
		if t.Tag == running {
			b.RunningPushedAt = t.PushedAt
		}
		if !found && !genericTags[strings.ToLower(t.Tag)] && !movingWord(t.Tag) {
			b.Tag, b.PushedAt, found = t.Tag, t.PushedAt, true
		}
	}
	if !found {
		return b, false
	}
	b.Newer = b.Tag != running && (b.RunningPushedAt.IsZero() || b.PushedAt.After(b.RunningPushedAt))
	return b, true
}

// movingWord is a tag that names a place rather than a build: dev, staging, prod, edge.
func movingWord(tag string) bool {
	switch strings.ToLower(tag) {
	case "dev", "develop", "development", "staging", "stage", "prod", "production", "edge", "nightly", "head", "master", "current":
		return true
	}
	return false
}
