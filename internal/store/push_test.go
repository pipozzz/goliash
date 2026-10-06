// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
)

func TestPushSubscriptionsAndServerSecret(t *testing.T) {
	forEachDialect(t, func(t *testing.T, s *Store) {
		ctx := context.Background()
		ws, _ := s.EnsureDefaultWorkspace(ctx)
		sc := ws.Scope()
		_ = s.SetSecretKey(make([]byte, SecretKeySize))
		ch, err := s.CreateChannel(ctx, Channel{Scope: sc, Type: "push", Name: "phones", Config: json.RawMessage(`{}`)})
		if err != nil {
			t.Fatal(err)
		}
		u, _ := s.CreateUser(ctx, sc.OrgID, "a@example.com", "", RoleMember)
		for range 2 { // the same browser twice: replaced, not duplicated
			if _, err := s.SavePushSubscription(ctx, sc, PushSubscription{ChannelID: ch.ID, UserID: u.ID, Endpoint: "https://fcm.googleapis.com/x", Keys: `{"auth":"k"}`, Label: "Chrome"}); err != nil {
				t.Fatal(err)
			}
		}
		subs, err := s.ListPushSubscriptions(ctx, sc, ch.ID)
		if err != nil || len(subs) != 1 || subs[0].Keys != `{"auth":"k"}` {
			t.Fatalf("subs %v %v", subs, err)
		}
		all, mine, _ := s.PushCounts(ctx, sc, u.ID)
		if all[ch.ID] != 1 || mine[ch.ID] != 1 {
			t.Fatalf("counts %v %v", all, mine)
		}
		if ok, _ := s.DeletePushSubscription(ctx, sc, ch.ID, "https://fcm.googleapis.com/x", "someone-else"); ok {
			t.Fatal("another user removed the subscription")
		}
		if ok, _ := s.DeletePushSubscription(ctx, sc, ch.ID, "https://fcm.googleapis.com/x", u.ID); !ok {
			t.Fatal("not removed")
		}

		calls := 0
		gen := func() (string, error) { calls++; return fmt.Sprintf("value-%d", calls), nil }
		a, err := s.ServerSecret(ctx, "k", gen)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := s.ServerSecret(ctx, "k", gen)
		if a != "value-1" || b != a || calls != 1 {
			t.Fatalf("server secret %q %q after %d", a, b, calls)
		}
	})
}
