// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func TestRecentDeliveries(t *testing.T) {
	forEachDialect(t, func(t *testing.T, s *Store) {
		ctx := context.Background()
		ws, _ := s.EnsureDefaultWorkspace(ctx)
		sc := ws.Scope()
		ch, _ := s.CreateChannel(ctx, Channel{Scope: sc, Type: "slack", Name: "ops", Config: json.RawMessage(`{}`)})
		r, _ := s.CreateRule(ctx, Rule{Scope: sc, ChannelID: ch.ID, Mode: "instant"})
		for _, k := range []string{"a", "b"} {
			if _, err := s.Enqueue(ctx, QueueItem{Scope: sc, RuleID: r.ID, Payload: json.RawMessage(`{"text":"` + k + `"}`), DedupKey: k, DueAt: time.Now()}); err != nil {
				t.Fatal(err)
			}
		}
		ds, err := s.RecentDeliveries(ctx, sc, 10)
		if err != nil || len(ds) != 2 || ds[0].ChannelName != "ops" {
			t.Fatalf("%v %v", ds, err)
		}
		_ = s.MarkSent(ctx, []string{ds[0].ID})
		if ok, _ := s.RetryDelivery(ctx, sc, ds[0].ID); ok {
			t.Fatal("a sent notification was retried")
		}
		_ = s.MarkFailed(ctx, []string{ds[1].ID}, "boom", time.Now().Add(time.Hour))
		if ok, _ := s.RetryDelivery(ctx, sc, ds[1].ID); !ok {
			t.Fatal("not retried")
		}
		ds, _ = s.RecentDeliveries(ctx, sc, 10)
		var sent int
		for _, d := range ds {
			if d.SentAt != nil {
				sent++
			}
			if d.LastError == "boom" && d.Attempts != 0 {
				t.Fatalf("attempts not reset: %d", d.Attempts)
			}
		}
		if sent != 1 {
			t.Fatalf("sent %d", sent)
		}
	})
}
