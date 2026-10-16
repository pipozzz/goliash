// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"context"
	"errors"
	"testing"
)

func TestSavedViewStore(t *testing.T) {
	forEachDialect(t, func(t *testing.T, s *Store) {
		ctx := context.Background()
		f := setup(t, s)
		sc := f.ws.Scope()
		shared, err := s.CreateSavedView(ctx, SavedView{Scope: sc, Name: "payments", URL: "/tiles?app=payments"})
		if err != nil {
			t.Fatal(err)
		}
		mine, _ := s.CreateSavedView(ctx, SavedView{Scope: sc, UserID: "u1", Name: "wall", URL: "/tiles?tv=1"})
		_, _ = s.CreateSavedView(ctx, SavedView{Scope: sc, UserID: "u2", Name: "theirs", URL: "/tiles"})
		got, err := s.ListSavedViews(ctx, sc, "u1")
		if err != nil || len(got) != 2 || got[0].ID != shared.ID || got[0].UserID != "" || got[1].ID != mine.ID || got[1].UserID != "u1" {
			t.Fatalf("views %+v %v", got, err)
		}
		if v, err := s.GetSavedView(ctx, sc, mine.ID); err != nil || v.URL != "/tiles?tv=1" {
			t.Fatalf("get %+v %v", v, err)
		}
		if err := s.DeleteSavedView(ctx, sc, mine.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := s.GetSavedView(ctx, sc, mine.ID); !errors.Is(err, ErrNotFound) {
			t.Fatalf("deleted: %v", err)
		}
	})
}
