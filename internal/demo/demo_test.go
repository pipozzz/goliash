// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package demo

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/pipozzz/goliash/internal/store"
	"github.com/pipozzz/goliash/internal/versions"
)

func TestSeed(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, "sqlite://:memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	ws, _ := st.EnsureDefaultWorkspace(ctx)
	if err := Seed(ctx, st, ws, log); err != nil {
		t.Fatal(err)
	}
	if err := Seed(ctx, st, ws, log); !errors.Is(err, ErrAlreadySeeded) {
		t.Fatalf("second seed: %v", err)
	}
	o, err := versions.LoadOverview(ctx, st, ws.Scope())
	if err != nil {
		t.Fatal(err)
	}
	if len(o.Stale) != 0 {
		t.Fatalf("demo data looks stale: %v", o.Stale)
	}
	if len(o.Matrix.Rows) != 18 || o.Matrix.Unmapped != 1 {
		t.Fatalf("rows=%d unmapped=%d", len(o.Matrix.Rows), o.Matrix.Unmapped)
	}
	kinds := map[string]int{}
	for _, ds := range o.Drifts {
		for _, d := range ds {
			kinds[d.Kind]++
		}
	}
	if kinds["env"] == 0 || kinds["upstream"] == 0 || kinds["inconsistent"] == 0 {
		t.Fatalf("drift kinds %v", kinds)
	}
	evs, _ := st.ListEvents(ctx, ws.Scope(), store.EventFilter{Limit: 500})
	types := map[string]int{}
	for _, e := range evs {
		types[e.Type]++
	}
	if types["version_changed"] < 5 || types["new_release"] == 0 || types["drift_detected"] == 0 {
		t.Fatalf("event types %v", types)
	}
	for _, row := range o.Matrix.Rows {
		if row.Service.Name == "postgres" {
			u := o.Upstreams[row.Service.ID]
			if u.Latest.Raw != "15.8" || u.LatestAny.Raw != "17.0" {
				t.Fatalf("postgres upstream %+v", u)
			}
		}
		if strings.HasPrefix(row.Service.Name, "istio") {
			t.Fatal("sidecar became a service")
		}
	}
}
