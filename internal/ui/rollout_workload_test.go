// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package ui

import (
	"testing"

	"github.com/pipozzz/goliash/internal/versions"
)

func TestRolloutWorkload(t *testing.T) {
	for _, c := range []struct{ svc, workload, want string }{
		{"postgres", "postgres", ""},
		{"postgres", "db/postgres", ""},
		{"postgres", "media/postgres-compress-driver", ", media/postgres-compress-driver"},
		{"postgres", "", ""},
	} {
		if got := rolloutWorkload(versions.RolloutIssue{Service: c.svc, Workload: c.workload}); got != c.want {
			t.Errorf("rolloutWorkload(%q, %q) = %q, want %q", c.svc, c.workload, got, c.want)
		}
	}
}
