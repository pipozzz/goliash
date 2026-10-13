// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package notifier

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/pipozzz/goliash/internal/store"
)

func TestPlanOpensWithGolisTake(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	slack := newSink(t)
	e.rule(e.channel("slack", "plan", map[string]any{"url": slack.srv.URL}), "weekly", []string{EventUpdatesPlan}, Filter{})
	rules, _ := e.st.ListRules(ctx, e.sc)
	if _, err := e.st.OpenDrift(ctx, store.Drift{
		Scope: e.sc, ServiceID: e.db.ID, EnvironmentID: e.prod.ID, Kind: "upstream",
		Detail: json.RawMessage(`{"running":"15.6","other":"16.4","jump":"major"}`),
	}); err != nil {
		t.Fatal(err)
	}
	var asked []string
	e.n.SetTake(func(_ context.Context, workspace string, items []Item) (string, error) {
		asked = append(asked, workspace)
		if len(items) != 1 || !strings.Contains(items[0].Text, "15.6") {
			t.Errorf("items %+v", items)
		}
		return "  Upgrade postgres in prod first: it is a major jump.  ", nil
	})
	if _, err := e.n.SendPlanNow(ctx, e.sc, rules[0].ID); err != nil {
		t.Fatal(err)
	}
	if len(asked) != 1 || !strings.Contains(slack.last(), "Goli's take") || !strings.Contains(slack.last(), "Upgrade postgres in prod first") {
		t.Fatalf("asked %v, sent %s", asked, slack.last())
	}

	// Without a take the plan still goes.
	e.n.SetTake(func(context.Context, string, []Item) (string, error) { return "", errors.New("provider down") })
	if _, err := e.n.SendPlanNow(ctx, e.sc, rules[0].ID); err != nil {
		t.Fatal(err)
	}
	if slack.count() != 2 || strings.Contains(slack.last(), "Goli's take") {
		t.Errorf("sent %d: %s", slack.count(), slack.last())
	}
}

func TestTakeOnlyForPlans(t *testing.T) {
	n := &Notifier{log: slog.New(slog.DiscardHandler)}
	called := false
	n.SetTake(func(context.Context, string, []Item) (string, error) { called = true; return "x", nil })
	msg := Message{Items: []Item{{Type: "new_release", Text: "nginx 1.27.3 is out", At: time.Now()}}}
	n.addTake(context.Background(), &msg)
	if called || msg.Take != "" {
		t.Error("a take on a message that is no plan")
	}
}

func TestTakeRendered(t *testing.T) {
	msg := Message{Workspace: "Prod", Digest: true, Take: "Upgrade <payments> first.", Items: []Item{{Type: "update", Service: "payments", Environment: "prod", From: "1", To: "2", Text: "payments in prod: 1 → 2", At: time.Now()}}}
	page, err := emailHTML(msg)
	if err != nil || !strings.Contains(page, "Goli's take:") || !strings.Contains(page, "Upgrade &lt;payments&gt; first.") {
		t.Errorf("email: %v", err)
	}
	if b, _ := json.Marshal(slackBlocks(msg)); !strings.Contains(string(b), "Goli's take:") {
		t.Errorf("slack blocks %s", b)
	}
	if _, body := lines(msg, func(label, _ string) string { return label }); body[0] != "Goli's take: Upgrade <payments> first." {
		t.Errorf("chat lines %v", body)
	}
	if p := pushPayload(msg); !strings.HasPrefix(p.Body, "Upgrade <payments> first.") {
		t.Errorf("push %q", p.Body)
	}
	if b, _ := json.Marshal(teamsCard(msg)); !strings.Contains(string(b), "Goli's take") {
		t.Errorf("teams %s", b)
	}
}
