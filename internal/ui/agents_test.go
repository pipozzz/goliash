// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package ui

import (
	"testing"

	"github.com/pipozzz/goliash/pkg/buildinfo"
)

func TestOlderThanServer(t *testing.T) {
	old := buildinfo.Version
	t.Cleanup(func() { buildinfo.Version = old })

	buildinfo.Version = "dev"
	if olderThanServer("v0.1.0") {
		t.Fatal("a development server called an agent outdated")
	}
	buildinfo.Version = "v0.4.0"
	for agent, want := range map[string]bool{"v0.3.0": true, "0.3.9": true, "v0.4.0": false, "v0.5.0": false, "dev": false, "": false} {
		if got := olderThanServer(agent); got != want {
			t.Errorf("agent %q: %v", agent, got)
		}
	}
}
