// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package versions

import (
	"strings"
	"testing"

	"github.com/pipozzz/goliash/internal/store"
)

func TestBetween(t *testing.T) {
	var rels []store.Release
	for _, v := range strings.Fields("1.4.2 1.5.0 1.5.1 1.6.0 1.6.0-alpine 1.7.0-rc.1 1.7.0 1.4.2-alpine") {
		rels = append(rels, store.Release{Version: v})
	}
	names := func(rs []store.Release) string {
		var out []string
		for _, r := range rs {
			out = append(out, r.Version)
		}
		return strings.Join(out, " ")
	}
	if got := names(between(rels, "1.4.2", "1.6.0")); got != "1.6.0 1.5.1 1.5.0" {
		t.Fatalf("between 1.4.2 and 1.6.0: %s", got)
	}
	if got := names(between(rels, "1.4.2-alpine", "1.6.0-alpine")); got != "1.6.0-alpine" {
		t.Fatalf("variants compare like with like: %s", got)
	}
	if got := names(between(rels, "1.6.0", "1.7.0")); got != "1.7.0" {
		t.Fatalf("prereleases are left out unless waiting: %s", got)
	}
	if got := between(rels, "latest", "1.7.0"); got != nil {
		t.Fatalf("unparseable running version: %v", got)
	}
}
