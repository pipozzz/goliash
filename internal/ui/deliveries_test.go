// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package ui

import (
	"context"
	"encoding/json"
	"net/url"
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
	queue("db: new release 17.2", time.Now().Add(20*time.Hour)) // an instant rule: held for quiet hours

	member := e.as(store.RoleMember)
	_, page := get(t, member, e.srv.URL+"/notifications", nil)
	for _, want := range []string{"Recent deliveries", "gave up after 6 attempts", "answered 404: no_team", "Retry now", ">sent<", "held for quiet hours, out in", "Send now"} {
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

func TestSentNotifications(t *testing.T) {
	e := newUIEnv(t)
	ctx := context.Background()
	sc := e.ws.Scope()
	ops, _ := e.st.CreateChannel(ctx, store.Channel{Scope: sc, Type: "slack", Name: "ops", Config: json.RawMessage(`{"url":"https://hooks.slack.com/x"}`)})
	mail, _ := e.st.CreateChannel(ctx, store.Channel{Scope: sc, Type: "email", Name: "security-mail", Config: json.RawMessage(`{"to":["sec@example.com"]}`)})
	opsRule, _ := e.st.CreateRule(ctx, store.Rule{Scope: sc, ChannelID: ops.ID, Mode: "instant"})
	mailRule, _ := e.st.CreateRule(ctx, store.Rule{Scope: sc, ChannelID: mail.ID, Mode: "daily"})
	n := 0
	queue := func(rule, text string, due time.Time) string {
		n++
		payload, _ := json.Marshal(notifier.Item{Type: "new_release", Text: text})
		if _, err := e.st.Enqueue(ctx, store.QueueItem{Scope: sc, RuleID: rule, Payload: payload, DedupKey: text, DueAt: due}); err != nil {
			t.Fatal(err)
		}
		ds, _ := e.st.RecentDeliveries(ctx, sc, 1)
		return ds[0].ID
	}
	for i := range 40 {
		id := queue(opsRule.ID, "svc"+itoa(i)+": new release 1.0", time.Now())
		_ = e.st.MarkSent(ctx, []string{id})
	}
	failing := queue(mailRule.ID, "payments-api: new release 1.6.0", time.Now())
	_ = e.st.MarkFailed(ctx, []string{failing}, "smtp: 550 mailbox unavailable", time.Now().Add(time.Hour))
	queue(mailRule.ID, "db: new release 17.2", time.Now().Add(20*time.Hour))

	member := e.as(store.RoleMember)
	_, page := get(t, member, e.srv.URL+"/notifications", nil)
	if !strings.Contains(page, `href="/notifications/deliveries"`) {
		t.Error("no way to the full list")
	}
	_, page = get(t, member, e.srv.URL+"/notifications/deliveries", nil)
	// All 42, not just the latest 25 the Notifications page shows.
	if strings.Count(page, `class="delivery-text"`) != 42 || !strings.Contains(page, "all (42)") || !strings.Contains(page, "retrying (1)") {
		t.Errorf("full list: %d rows", strings.Count(page, `class="delivery-text"`))
	}
	_, page = get(t, member, e.srv.URL+"/notifications/deliveries?status=retrying", nil)
	if strings.Count(page, `class="delivery-text"`) != 1 || !strings.Contains(page, "550 mailbox unavailable") {
		t.Error("status filter")
	}
	_, page = get(t, member, e.srv.URL+"/notifications/deliveries?channel="+mail.ID, nil)
	if strings.Count(page, `class="delivery-text"`) != 2 {
		t.Errorf("channel filter: %d rows", strings.Count(page, `class="delivery-text"`))
	}
	_, page = get(t, member, e.srv.URL+"/notifications/deliveries?q=svc1", nil)
	if c := strings.Count(page, `class="delivery-text"`); c != 11 { // svc1, svc10-svc19
		t.Errorf("text filter: %d rows", c)
	}
	// A retry from this page comes back to it, filter and all.
	_, body, _ := post(t, member, e.srv.URL+"/notifications/deliveries/"+failing+"/retry", url.Values{"return": {"/notifications/deliveries?status=retrying"}})
	if !strings.Contains(body, "<h1>Delivery log</h1>") {
		t.Error("retry did not come back to the log")
	}
	if _, body, _ := post(t, member, e.srv.URL+"/notifications/deliveries/"+failing+"/retry", url.Values{"return": {"https://evil.example/"}}); !strings.Contains(body, "<h1>Notifications</h1>") {
		t.Error("a foreign return was followed")
	}
}
