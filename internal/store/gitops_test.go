// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"context"
	"testing"
	"time"
)

func TestWorkloadGitOps(t *testing.T) {
	forEachDialect(t, func(t *testing.T, s *Store) {
		ctx := context.Background()
		ws, _ := s.EnsureDefaultWorkspace(ctx)
		sc := ws.Scope()
		env, _ := s.CreateEnvironment(ctx, sc, "prod", 30)
		tgt, err := s.CreateTarget(ctx, Target{Scope: sc, EnvironmentID: env.ID, Platform: "kubernetes", Name: "k8s"})
		if err != nil {
			t.Fatal(err)
		}
		t0 := time.Date(2026, 10, 18, 10, 0, 0, 0, time.UTC)
		a := WorkloadGitOps{WorkloadID: "w1", Tool: "argocd", App: "api", Repo: "https://github.com/acme/deploy", Sync: "OutOfSync", Health: "Healthy"}
		b := WorkloadGitOps{WorkloadID: "w2", Tool: "argocd", App: "web", Sync: "Synced", Health: "Healthy"}
		if err := s.SetWorkloadGitOps(ctx, sc, tgt.ID, []WorkloadGitOps{a, b}, true, t0); err != nil {
			t.Fatal(err)
		}
		// The same state an hour later keeps its start; a change starts again; a complete snapshot without w2 drops it.
		a.Revision = "abc1234"
		if err := s.SetWorkloadGitOps(ctx, sc, tgt.ID, []WorkloadGitOps{a}, true, t0.Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
		got, err := s.ListWorkloadGitOps(ctx, sc)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 1 || got[0].WorkloadID != "w1" || !got[0].SyncSince.Equal(t0) || got[0].Revision != "abc1234" || got[0].TargetID != tgt.ID {
			t.Fatalf("after a complete snapshot %+v", got)
		}
		a.Sync = "Synced"
		if err := s.SetWorkloadGitOps(ctx, sc, tgt.ID, []WorkloadGitOps{a}, false, t0.Add(2*time.Hour)); err != nil {
			t.Fatal(err)
		}
		if got, _ = s.ListWorkloadGitOps(ctx, sc); len(got) != 1 || !got[0].SyncSince.Equal(t0.Add(2*time.Hour)) || got[0].Sync != "Synced" {
			t.Fatalf("after a change %+v", got)
		}
		// The first revision seen has nothing before it; a new one remembers the one it replaced, and when.
		if got[0].PrevRevision != "" || !got[0].RevisionAt.IsZero() {
			t.Fatalf("a previous revision before any change %+v", got[0])
		}
		a.Revision = "def5678"
		if err := s.SetWorkloadGitOps(ctx, sc, tgt.ID, []WorkloadGitOps{a}, true, t0.Add(3*time.Hour)); err != nil {
			t.Fatal(err)
		}
		if err := s.SetWorkloadGitOps(ctx, sc, tgt.ID, []WorkloadGitOps{a}, true, t0.Add(4*time.Hour)); err != nil {
			t.Fatal(err)
		}
		if got, _ = s.ListWorkloadGitOps(ctx, sc); got[0].PrevRevision != "abc1234" || !got[0].RevisionAt.Equal(t0.Add(3*time.Hour)) {
			t.Fatalf("after Git moved %+v", got[0])
		}
		// A sync window blocking it is kept, and stops counting once it has closed.
		until := t0.Add(6 * time.Hour)
		a.SyncBlocked, a.SyncBlockedUntil, a.ManualSync = `deny window "0 22 * * 5" for 56h`, until, true
		if err := s.SetWorkloadGitOps(ctx, sc, tgt.ID, []WorkloadGitOps{a}, true, t0.Add(5*time.Hour)); err != nil {
			t.Fatal(err)
		}
		got, _ = s.ListWorkloadGitOps(ctx, sc)
		if !got[0].ManualSync || got[0].SyncBlocked != a.SyncBlocked || !got[0].SyncBlockedUntil.Equal(until) || !got[0].Blocked(t0.Add(5*time.Hour)) || got[0].Blocked(until) {
			t.Fatalf("sync window %+v", got[0])
		}
	})
}
