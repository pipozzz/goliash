// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package ui

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/pipozzz/goliash/internal/notifier"
	"github.com/pipozzz/goliash/internal/store"
)

func TestRecentDeliveries(t *testing.T) {
	e := newUIEnv(t)
	ctx := context.Background()
	sc := e.ws.Scope()
	ch, _ := e.st.CreateChannel(ctx, store.Channel{Scope: sc, Type: "slack", Name: "ops", Config: json.RawMessage(`{"url":"https://hooks.slack.com/x"}`)})
	rule, _ := e.st.CreateRule(ctx, store.Rule{Scope: sc, ChannelID: ch.ID, Mode: "instant"})
	queue := func(text string, due time.Time) string {
		payload, _ := json.Marshal(notifier.Item{Type: "new_release", Text: text})
		if _, err := e.st.Enqueue(ctx, store.QueueItem{Scope: sc, RuleID: rule.ID, Payload: payload, DedupKey: text, DueAt: due}); err != nil {
			t.Fatal(err)
		}
		ds, _ := e.st.RecentDeliveries(ctx, sc, 10)
		return ds[0].ID
	}
	failed := queue("payments-api: new release 1.6.0", time.Now())
	for range notifier.MaxAttempts {
		_ = e.st.MarkFailed(ctx, []string{failed}, "answered 404: no_team", time.Now())
	}
	sent := queue("web: new release 2.0.0", time.Now())
	_ = e.st.MarkSent(ctx, []string{sent})
	queue("db: new release 17.2", time.Now().Add(20*time.Hour))

	member := e.as(store.RoleMember)
	_, page := get(t, member, e.srv.URL+"/notifications", nil)
	for _, want := range []string{"Recent deliveries", "gave up after 6 attempts", "answered 404: no_team", "Retry now", ">sent<", "in the digest in", "Send now"} {
		if !strings.Contains(page, want) {
			t.Errorf("page misses %q", want)
		}
	}
	// Retrying sends at once; without a Slack sender here it fails again, and says so.
	if _, body, _ := post(t, member, e.srv.URL+"/notifications/deliveries/"+failed+"/retry", nil); !strings.Contains(body, "Sending to ops failed again") {
		t.Fatalf("retry: %s", body)
	}
	if _, body, _ := post(t, member, e.srv.URL+"/notifications/deliveries/"+sent+"/retry", nil); !strings.Contains(body, "already sent") {
		t.Fatalf("retry of a sent one: %s", body)
	}
	if _, page = get(t, e.as(store.RoleViewer), e.srv.URL+"/notifications", nil); strings.Contains(page, "Retry now") {
		t.Error("a viewer is offered Retry")
	}
}
