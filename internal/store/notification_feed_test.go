// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func TestNotificationFeed(t *testing.T) {
	forEachDialect(t, func(t *testing.T, s *Store) {
		ctx := context.Background()
		f := setup(t, s)
		sc := f.ws.Scope()
		user, err := s.CreateUser(ctx, f.ws.OrgID, "reader@example.com", "", RoleViewer)
		if err != nil {
			t.Fatal(err)
		}
		slack, err := s.CreateChannel(ctx, Channel{Scope: sc, Type: "slack", Name: "ops", Config: json.RawMessage(`{}`)})
		if err != nil {
			t.Fatal(err)
		}
		app, err := s.CreateChannel(ctx, Channel{Scope: sc, Type: "inapp", Name: "goliash", Config: json.RawMessage(`{}`)})
		if err != nil {
			t.Fatal(err)
		}
		toSlack, _ := s.CreateRule(ctx, Rule{Scope: sc, ChannelID: slack.ID, Mode: "instant"})
		toApp, _ := s.CreateRule(ctx, Rule{Scope: sc, ChannelID: app.ID, Mode: "instant"})
		digest, _ := s.CreateRule(ctx, Rule{Scope: sc, ChannelID: app.ID, Mode: "daily"})
		send := func(rule, key, payload string) {
			t.Helper()
			if _, err := s.Enqueue(ctx, QueueItem{Scope: sc, RuleID: rule, Payload: json.RawMessage(payload), DedupKey: key, DueAt: time.Now()}); err != nil {
				t.Fatal(err)
			}
			due, _ := s.DueItems(ctx, time.Now().Add(time.Minute), 6)
			var ids []string
			for _, d := range due {
				ids = append(ids, d.ID)
			}
			if err := s.MarkSent(ctx, ids); err != nil {
				t.Fatal(err)
			}
		}
		// The same alert through two channels is one notification.
		send(toSlack.ID, "a", `{"type":"vulnerability","text":"CVE-1 in web"}`)
		send(toApp.ID, "a", `{"type":"vulnerability","text":"CVE-1 in web"}`)
		time.Sleep(5 * time.Millisecond)
		// A digest of two items, sent at once, is one notification.
		if _, err := s.Enqueue(ctx, QueueItem{Scope: sc, RuleID: digest.ID, Payload: json.RawMessage(`{"text":"d1"}`), DedupKey: "d1", DueAt: time.Now()}); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Enqueue(ctx, QueueItem{Scope: sc, RuleID: digest.ID, Payload: json.RawMessage(`{"text":"d2"}`), DedupKey: "d2", DueAt: time.Now()}); err != nil {
			t.Fatal(err)
		}
		due, _ := s.DueItems(ctx, time.Now().Add(time.Minute), 6)
		var ids []string
		for _, d := range due {
			ids = append(ids, d.ID)
		}
		if err := s.MarkSent(ctx, ids); err != nil {
			t.Fatal(err)
		}

		feed, err := s.NotificationFeed(ctx, sc, time.Time{}, 50)
		if err != nil {
			t.Fatal(err)
		}
		if len(feed) != 2 || len(feed[0].Items) != 2 || feed[0].Mode != "daily" || len(feed[1].Items) != 1 {
			t.Fatalf("feed %+v", feed)
		}

		if at, _ := s.NotificationsSeen(ctx, sc, user.ID); !at.IsZero() {
			t.Errorf("seen %v before looking", at)
		}
		if err := s.SeeNotifications(ctx, sc, user.ID); err != nil {
			t.Fatal(err)
		}
		at, _ := s.NotificationsSeen(ctx, sc, user.ID)
		if at.IsZero() {
			t.Fatal("not seen after looking")
		}
		if err := s.SeeNotifications(ctx, sc, user.ID); err != nil {
			t.Fatal("seeing twice:", err)
		}
		if feed, _ := s.NotificationFeed(ctx, sc, at, 50); len(feed) != 0 {
			t.Errorf("unread after looking: %d", len(feed))
		}
	})
}
