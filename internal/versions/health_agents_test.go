// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package versions

import (
	"context"
	"strings"
	"testing"

	"github.com/pipozzz/goliash/internal/store"
)

// An agent left on an older release than the server is a warning on the Status page.
func TestHealthOlderAgents(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, "sqlite://:memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	ws, _ := st.EnsureDefaultWorkspace(ctx)
	sc := ws.Scope()
	for name, version := range map[string]string{"edge": "1.70.0", "core": "1.74.0", "lab": "dev"} {
		a, err := st.CreateAgent(ctx, sc, name, "hash-"+name)
		if err != nil {
			t.Fatal(err)
		}
		if err := st.RegisterAgent(ctx, sc, a.ID, version, "host", []string{"nomad"}); err != nil {
			t.Fatal(err)
		}
	}
	h, err := LoadHealth(ctx, st, nil, sc, "1.74.0", false)
	if err != nil {
		t.Fatal(err)
	}
	c := h.Checks[0]
	if c.Title != "Agents" || c.State != "warn" || !strings.Contains(c.Summary, "1 of 3 on an older release") ||
		len(c.Details) != 1 || c.Details[0] != "edge: runs 1.70.0, the server 1.74.0" {
		t.Fatalf("agents check %+v", c)
	}
	// A development server compares nothing.
	if h, _ = LoadHealth(ctx, st, nil, sc, "dev", false); h.Checks[0].State != "ok" {
		t.Errorf("dev server: %+v", h.Checks[0])
	}
}
