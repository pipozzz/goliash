// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"context"
	"encoding/json"
	"testing"
)

func TestMergeService(t *testing.T) {
	forEachDialect(t, func(t *testing.T, s *Store) {
		ctx := context.Background()
		ws, _ := s.EnsureDefaultWorkspace(ctx)
		sc := ws.Scope()
		env, _ := s.CreateEnvironment(ctx, sc, "prod", 10)
		from, _ := s.EnsureService(ctx, sc, "cefiro-redis")
		into, _ := s.EnsureService(ctx, sc, "redis")
		_, _ = s.InsertReleases(ctx, sc, from.ID, []string{"7.4.0", "7.4.1"})
		_, _ = s.InsertReleases(ctx, sc, into.ID, []string{"7.4.0"})
		for _, d := range []Drift{
			{Scope: sc, ServiceID: from.ID, EnvironmentID: env.ID, Kind: "upstream", Detail: json.RawMessage(`{}`)},
			{Scope: sc, ServiceID: into.ID, EnvironmentID: env.ID, Kind: "upstream", Detail: json.RawMessage(`{}`)},
			{Scope: sc, ServiceID: from.ID, EnvironmentID: env.ID, Kind: "eol", Detail: json.RawMessage(`{}`)},
		} {
			if _, err := s.OpenDrift(ctx, d); err != nil {
				t.Fatal(err)
			}
		}
		if err := s.MergeService(ctx, sc, from.ID, into.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := s.GetService(ctx, sc, from.ID); err == nil {
			t.Fatal("merged service kept")
		}
		if rel, _ := s.ListReleases(ctx, sc, into.ID); len(rel) != 2 {
			t.Fatalf("releases after merge: %+v", rel)
		}
		open, _ := s.OpenDrifts(ctx, sc)
		kinds := map[string]bool{}
		for _, d := range open {
			if d.ServiceID != into.ID {
				t.Fatalf("drift of %s left", d.ServiceID)
			}
			kinds[d.Kind] = true
		}
		if len(open) != 2 || !kinds["upstream"] || !kinds["eol"] {
			t.Fatalf("open drift after merge: %+v", open)
		}
	})
}
