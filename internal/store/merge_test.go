// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"context"
	"encoding/json"
	"testing"
	"time"
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

func TestMoveAppInstances(t *testing.T) {
	forEachDialect(t, func(t *testing.T, s *Store) {
		ctx := context.Background()
		f := setup(t, s)
		sc := f.ws.Scope()
		from, _ := s.EnsureService(ctx, sc, "chat-db")
		to, _ := s.EnsureService(ctx, sc, "portal-db")
		now := time.Now()
		var ids []string
		for _, app := range []string{"mattermost", "portal"} {
			id := NewID()
			ids = append(ids, id)
			if err := s.ApplySnapshot(ctx, SnapshotChanges{Scope: sc, SnapshotID: NewID(), TargetID: f.tgt.ID, At: now, Upsert: []Instance{{
				ID: id, TargetID: f.tgt.ID, EnvironmentID: f.env.ID, WorkloadID: app + "/db", WorkloadName: "db", ContainerName: "db",
				Image: "postgres:17", Tag: "17", Running: 1, IsMain: true, ServiceID: from.ID, App: app, AppSource: "compose project",
			}}, Events: []Event{{Scope: sc, Type: "deployed", ServiceID: from.ID, EnvironmentID: f.env.ID, TargetID: f.tgt.ID, InstanceID: id, ToVersion: "17", At: now}}}); err != nil {
				t.Fatal(err)
			}
		}
		n, err := s.MoveAppInstances(ctx, sc, from.ID, to.ID, "portal")
		if err != nil || n != 1 {
			t.Fatalf("moved %d %v", n, err)
		}
		evs, _ := s.ListEvents(ctx, sc, EventFilter{ServiceID: to.ID, Limit: 10})
		if len(evs) != 1 || evs[0].InstanceID != ids[1] {
			t.Fatalf("events %+v", evs)
		}
		if evs, _ := s.ListEvents(ctx, sc, EventFilter{ServiceID: from.ID, Limit: 10}); len(evs) != 1 {
			t.Fatalf("events left %+v", evs)
		}
	})
}
