// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package notifier

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/pipozzz/goliash/internal/store"
	"github.com/pipozzz/goliash/internal/versions"
)

func TestPostureReport(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	ciso := newSink(t)
	everything := newSink(t)
	e.rule(e.channel("slack", "ciso", map[string]any{"url": ciso.srv.URL}), "weekly", []string{EventPostureReport}, Filter{})
	// A rule for every event does not get the report: it is asked for by name, like the plan.
	e.rule(e.channel("slack", "all", map[string]any{"url": everything.srv.URL}), "instant", nil, Filter{})
	if _, err := e.st.OpenDrift(ctx, store.Drift{
		Scope: e.sc, ServiceID: e.db.ID, EnvironmentID: e.prod.ID, Kind: "eol",
		Detail: json.RawMessage(`{"running":"13.4","other":"13","eol":"2025-11-13"}`),
	}); err != nil {
		t.Fatal(err)
	}

	e.clock = time.Date(2026, 10, 5, 9, 5, 0, 0, time.UTC) // Monday, after 8:00
	for range 2 {
		if err := e.n.PlanUpdates(ctx); err != nil {
			t.Fatal(err)
		}
		_ = e.n.DeliverDue(ctx)
	}
	if ciso.count() != 1 {
		t.Fatalf("report sent %d times", ciso.count())
	}
	msg := ciso.last()
	for _, want := range []string{"security posture", "prod runs", "End of life: 1 passed", "Accepted risk: 0", "postgres 13.4"} {
		if !strings.Contains(msg, want) {
			t.Errorf("report misses %q: %s", want, msg)
		}
	}
	if everything.count() > 0 && strings.Contains(everything.last(), "posture") {
		t.Error("a rule for every event got the report")
	}

	// Send now, for checking the channel; it shows under the bell.
	rules, _ := e.st.ListRules(ctx, e.sc)
	var report, other string
	for _, r := range rules {
		if contains(r.EventTypes, EventPostureReport) {
			report = r.ID
		} else {
			other = r.ID
		}
	}
	// With a reading from a week before, the report says where the numbers were.
	if err := e.st.RecordPosture(ctx, e.sc, store.PostureSample{Day: "2026-09-28", Exposed: 4, Median: 70, Behind90: 3, EOLPassed: 2}); err != nil {
		t.Fatal(err)
	}
	if err := e.n.SendPostureNow(ctx, e.sc, report); err != nil || ciso.count() != 2 {
		t.Fatalf("send now: %v, sent %d", err, ciso.count())
	}
	if !strings.Contains(ciso.last(), "A week ago (2026-09-28): 4 behind, median 70 days, 3 behind 90+, 2 past end of life.") {
		t.Errorf("no week-ago numbers: %s", ciso.last())
	}
	// And the hourly reading records today's.
	if err := e.n.RecordPostures(ctx); err != nil {
		t.Fatal(err)
	}
	if hist, _ := e.st.PostureHistory(ctx, e.sc, "2026-10-05"); len(hist) != 1 || hist[0].EOLPassed != 1 {
		t.Errorf("recorded %+v", hist)
	}
	if feed, _ := e.st.NotificationFeed(ctx, e.sc, time.Time{}, 10); len(feed) == 0 {
		t.Error("the report sent now is not under the bell")
	}
	if err := e.n.SendPostureNow(ctx, e.sc, other); !errors.Is(err, ErrNotAReportRule) {
		t.Errorf("not a report rule: %v", err)
	}
}

func TestPostureItemOutdatedBases(t *testing.T) {
	pos := versions.Posture{Prod: store.Environment{Name: "prod"}, ProdRunning: 9, OutdatedBase: []string{"api", "checkout", "web", "worker"}}
	it := PostureItem(pos, nil, time.Now())
	if !strings.Contains(it.Text, "Built on an outdated base (rebuild with --pull): api, checkout, web, 1 more.") {
		t.Errorf("text: %s", it.Text)
	}
}
