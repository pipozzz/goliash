// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"context"
	"testing"
	"time"
)

func TestFreezes(t *testing.T) {
	forEachDialect(t, func(t *testing.T, s *Store) {
		ctx := context.Background()
		ws, _ := s.EnsureDefaultWorkspace(ctx)
		sc := ws.Scope()
		t0 := time.Date(2026, 12, 20, 0, 0, 0, 0, time.UTC)
		xmas, err := s.AddFreeze(ctx, Freeze{Scope: sc, StartsAt: t0, EndsAt: t0.AddDate(0, 0, 14), Note: "Christmas", CreatedBy: "sre@example.com"})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.AddFreeze(ctx, Freeze{Scope: sc, StartsAt: t0.AddDate(0, 0, -30), EndsAt: t0.AddDate(0, 0, -29), Note: "gone"}); err != nil {
			t.Fatal(err)
		}
		if _, ok, _ := s.ActiveFreeze(ctx, sc, t0.Add(-time.Hour)); ok {
			t.Fatal("active before it starts")
		}
		f, ok, err := s.ActiveFreeze(ctx, sc, t0.AddDate(0, 0, 3))
		if err != nil || !ok || f.Note != "Christmas" || !f.EndsAt.Equal(t0.AddDate(0, 0, 14)) {
			t.Fatalf("active %+v %v %v", f, ok, err)
		}
		if list, _ := s.ListFreezes(ctx, sc, t0); len(list) != 1 {
			t.Fatalf("list %+v", list)
		}
		if err := s.DeleteFreeze(ctx, sc, xmas.ID); err != nil {
			t.Fatal(err)
		}
		if _, ok, _ := s.ActiveFreeze(ctx, sc, t0.AddDate(0, 0, 3)); ok {
			t.Fatal("active once deleted")
		}
	})
}
