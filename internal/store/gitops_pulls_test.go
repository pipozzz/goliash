// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"context"
	"testing"
)

func TestGitOpsPulls(t *testing.T) {
	forEachDialect(t, func(t *testing.T, s *Store) {
		ctx := context.Background()
		ws, _ := s.EnsureDefaultWorkspace(ctx)
		sc := ws.Scope()
		p := GitOpsPull{
			Scope: sc, ServiceID: "svc", Environment: "prod", TargetID: "t", WorkloadID: "w", From: "1.4.2", To: "1.5.0",
			URL: "https://github.com/acme/deploy/pull/7", OpenedBy: "you@example.com",
		}
		if err := s.RecordGitOpsPull(ctx, p); err != nil {
			t.Fatal(err)
		}
		got, err := s.ListGitOpsPulls(ctx, sc)
		if err != nil || len(got) != 1 || got[0].URL != "https://github.com/acme/deploy/pull/7" || got[0].To != "1.5.0" || got[0].State != "open" {
			t.Fatalf("pulls %+v %v", got, err)
		}
		if err := s.SetGitOpsPullChecks(ctx, sc, got[0].ID, "failing"); err != nil {
			t.Fatal(err)
		}
		if got, _ = s.ListGitOpsPulls(ctx, sc); got[0].Checks != "failing" {
			t.Fatalf("checks %+v", got[0])
		}
		if err := s.SetGitOpsPullState(ctx, sc, got[0].ID, "merged"); err != nil {
			t.Fatal(err)
		}
		got, _ = s.ListGitOpsPulls(ctx, sc)
		mergedAt := got[0].MergedAt
		if got[0].State != "merged" || mergedAt.IsZero() {
			t.Fatalf("merged %+v", got[0])
		}
		if err := s.SetGitOpsPullState(ctx, sc, got[0].ID, "merged"); err != nil { // seen merged again: still when first seen
			t.Fatal(err)
		}
		if got, _ = s.ListGitOpsPulls(ctx, sc); !got[0].MergedAt.Equal(mergedAt) {
			t.Fatalf("merged_at moved %+v", got[0])
		}
		if err := s.SetGitOpsPullState(ctx, sc, got[0].ID, "closed"); err != nil {
			t.Fatal(err)
		}
		p.URL = "https://github.com/acme/deploy/pull/8" // closed, then opened again: the new one, open
		if err := s.RecordGitOpsPull(ctx, p); err != nil {
			t.Fatal(err)
		}
		if got, _ = s.ListGitOpsPulls(ctx, sc); len(got) != 1 || got[0].URL != "https://github.com/acme/deploy/pull/8" || got[0].State != "open" || got[0].Checks != "" {
			t.Fatalf("after opening again %+v", got)
		}
	})
}
