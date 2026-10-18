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
		p.URL = "https://github.com/acme/deploy/pull/8" // the same version again: the first stays
		if err := s.RecordGitOpsPull(ctx, p); err != nil {
			t.Fatal(err)
		}
		got, err := s.ListGitOpsPulls(ctx, sc)
		if err != nil || len(got) != 1 || got[0].URL != "https://github.com/acme/deploy/pull/7" || got[0].To != "1.5.0" {
			t.Fatalf("pulls %+v %v", got, err)
		}
	})
}
