// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"context"
	"testing"
	"time"
)

func TestSecurityWatches(t *testing.T) {
	forEachDialect(t, func(t *testing.T, s *Store) {
		ctx := context.Background()
		ws, _ := s.EnsureDefaultWorkspace(ctx)
		sc := ws.Scope()
		now := time.Now().UTC()
		w, err := s.AddWatch(ctx, SecurityWatch{Scope: sc, Term: "php", Note: "two critical fixes Oct 21", Until: now.Add(48 * time.Hour), CreatedBy: "alice@example.com"})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.AddWatch(ctx, SecurityWatch{Scope: sc, Term: "old", Until: now.Add(-time.Hour)}); err != nil {
			t.Fatal(err)
		}
		got, err := s.ListWatches(ctx, sc, now)
		if err != nil || len(got) != 1 || got[0].Term != "php" || got[0].Note != "two critical fixes Oct 21" || !got[0].Active(now) || got[0].Active(now.Add(72*time.Hour)) {
			t.Fatalf("watches %+v %v", got, err)
		}
		if err := s.DeleteWatch(ctx, sc, w.ID); err != nil {
			t.Fatal(err)
		}
		if got, _ = s.ListWatches(ctx, sc, now); len(got) != 0 {
			t.Fatalf("after delete %+v", got)
		}
	})
}
