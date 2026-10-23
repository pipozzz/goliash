// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package notifier

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/pipozzz/goliash/internal/store"
)

// Every workspace hears of exploited and critical vulnerabilities in production on the bell without setting anything
// up, once: a deleted default rule stays deleted. And it is told while nothing sends them outside Goliash.
func TestDefaultSecurityAlerts(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	if err := e.n.PlanUpdates(ctx); err != nil {
		t.Fatal(err)
	}
	rules, _ := e.st.ListRules(ctx, e.sc)
	if len(rules) != 1 || strings.Join(rules[0].EventTypes, ",") != "vulnerability,security_release" || !strings.Contains(string(rules[0].Filter), `"production":true`) {
		t.Fatalf("default rules %+v", rules)
	}
	channels, _ := e.st.ListChannels(ctx, e.sc)
	if len(channels) != 1 || channels[0].Type != "inapp" {
		t.Fatalf("default channel %+v", channels)
	}

	vuln := func(env store.Environment) store.Event {
		return store.Event{Type: "vulnerability", ServiceID: e.svc.ID, EnvironmentID: env.ID, FromVersion: "log4j-core 2.14.1", ToVersion: "2.15.0", Note: "CVE-2021-44228 exploited critical"}
	}
	e.emit(vuln(e.stg))
	if feed, _ := e.st.NotificationFeed(ctx, e.sc, time.Time{}, 10); len(feed) != 0 {
		t.Fatalf("a staging vulnerability reached the bell: %+v", feed)
	}
	e.emit(vuln(e.prod))
	feed, _ := e.st.NotificationFeed(ctx, e.sc, time.Time{}, 10)
	if len(feed) != 1 || !strings.Contains(string(feed[0].Items[0]), "CVE-2021-44228") {
		t.Fatalf("the production vulnerability is not on the bell: %+v", feed)
	}

	if out, _ := SecurityAlertsOutside(ctx, e.st, e.sc); out {
		t.Error("the bell counts as outside Goliash")
	}
	e.rule(e.channel("slack", "sec", map[string]any{"url": newSink(t).srv.URL}), "instant", []string{"vulnerability"}, Filter{})
	if out, _ := SecurityAlertsOutside(ctx, e.st, e.sc); !out {
		t.Error("a Slack rule for vulnerabilities is not seen")
	}

	// Deleted, it stays deleted.
	if err := e.st.DeleteRule(ctx, e.sc, rules[0].ID); err != nil {
		t.Fatal(err)
	}
	if err := e.n.PlanUpdates(ctx); err != nil {
		t.Fatal(err)
	}
	if rules, _ = e.st.ListRules(ctx, e.sc); len(rules) != 1 {
		t.Errorf("the default rule came back: %d rules", len(rules))
	}
}
