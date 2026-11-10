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

// The deploys brief says what went out since yesterday and what was rolled back, once a day; a quiet day sends nothing.
func TestDeliveryBrief(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	sink := newSink(t)
	e.rule(e.channel("slack", "ops", map[string]any{"url": sink.srv.URL}), "daily", []string{EventDeliveryBrief}, Filter{})
	e.clock = time.Now().UTC().Truncate(24 * time.Hour).Add(9*time.Hour + 5*time.Minute)

	if err := e.n.PlanUpdates(ctx); err != nil {
		t.Fatal(err)
	}
	_ = e.n.DeliverDue(ctx)
	if sink.count() != 0 {
		t.Fatalf("a quiet day was sent: %s", sink.last())
	}

	for _, ev := range []store.Event{
		{Type: "version_changed", ServiceID: e.svc.ID, EnvironmentID: e.prod.ID, FromVersion: "1.27.2", ToVersion: "1.28.0", At: e.clock.Add(-5 * time.Hour)},
		{Type: "version_changed", ServiceID: e.svc.ID, EnvironmentID: e.prod.ID, FromVersion: "1.28.0", ToVersion: "1.27.2", At: e.clock.Add(-3 * time.Hour)},
		{Type: "version_changed", ServiceID: e.svc.ID, EnvironmentID: e.prod.ID, FromVersion: "1.26.0", ToVersion: "1.27.0", At: e.clock.Add(-30 * time.Hour)}, // before yesterday
	} {
		if err := e.st.InsertEvent(ctx, e.sc, ev); err != nil {
			t.Fatal(err)
		}
	}
	for range 2 {
		if err := e.n.PlanUpdates(ctx); err != nil {
			t.Fatal(err)
		}
		_ = e.n.DeliverDue(ctx)
	}
	if sink.count() != 1 {
		t.Fatalf("want one brief, got %d", sink.count())
	}
	got := sink.last()
	for _, want := range []string{"Goliash deploys for", "2 deploys (prod 2)", "1 rollback: " + e.svc.Name + " 1.28.0 → 1.27.2 in prod"} {
		if !strings.Contains(got, want) {
			t.Errorf("brief misses %q: %s", want, got)
		}
	}
}
