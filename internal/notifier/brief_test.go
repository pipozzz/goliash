// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package notifier

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/pipozzz/goliash/internal/store"
	"github.com/pipozzz/goliash/internal/versions"
)

// The brief names new security releases of what runs and the watches, once a day, and nothing when there is nothing.
func TestSecurityBrief(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	sec := newSink(t)
	e.rule(e.channel("slack", "sec", map[string]any{"url": sec.srv.URL}), "daily", []string{EventSecurityBrief}, Filter{})
	e.clock = time.Now().UTC().Truncate(24 * time.Hour).Add(9*time.Hour + 5*time.Minute) // after 8:00, near when releases are found

	// Nothing to say, nothing sent.
	if err := e.n.PlanUpdates(ctx); err != nil {
		t.Fatal(err)
	}
	_ = e.n.DeliverDue(ctx)
	if sec.count() != 0 {
		t.Fatalf("an empty brief was sent: %s", sec.last())
	}

	tgt, err := e.st.CreateTarget(ctx, store.Target{Scope: e.sc, EnvironmentID: e.prod.ID, Platform: "kubernetes", Name: "k8s-prod"})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.st.ApplySnapshot(ctx, store.SnapshotChanges{
		Scope: e.sc, SnapshotID: store.NewID(), TargetID: tgt.ID, At: e.clock,
		Upsert: []store.Instance{{
			TargetID: tgt.ID, EnvironmentID: e.prod.ID, ServiceID: e.svc.ID, WorkloadID: "w1", WorkloadName: "shop",
			ContainerName: "php", Image: "docker.io/library/php:8.3.34-fpm", Tag: "8.3.34-fpm", Running: 1, IsMain: true,
		}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.InsertReleases(ctx, e.sc, e.svc.ID, []string{"8.3.33-fpm", "8.3.36-fpm"}); err != nil {
		t.Fatal(err)
	}
	for _, v := range []string{"8.3.33-fpm", "8.3.36-fpm"} {
		if err := e.st.SetReleaseSecurity(ctx, e.sc, e.svc.ID, v, "CVE-2026-1"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := e.st.AddWatch(ctx, store.SecurityWatch{Scope: e.sc, Term: "php"}); err != nil {
		t.Fatal(err)
	}

	for range 2 {
		if err := e.n.PlanUpdates(ctx); err != nil {
			t.Fatal(err)
		}
		_ = e.n.DeliverDue(ctx)
	}
	if sec.count() != 1 {
		t.Fatalf("brief sent %d times", sec.count())
	}
	msg := sec.last()
	for _, want := range []string{"1 security release since yesterday: payments-api 8.3.36-fpm (8.3.34-fpm runs)", "Watching “php” (1 service)", "Goliash security brief for "} {
		if !strings.Contains(msg, want) {
			t.Errorf("brief misses %q: %s", want, msg)
		}
	}
	if strings.Contains(msg, "8.3.33") {
		t.Errorf("an older release is named: %s", msg)
	}

	// Send now, and a rule that does not send the brief.
	rules, _ := e.st.ListRules(ctx, e.sc)
	if err := e.n.SendBriefNow(ctx, e.sc, rules[0].ID); err != nil || sec.count() != 2 {
		t.Fatalf("send now: %v, sent %d", err, sec.count())
	}
	e.rule(e.channel("slack", "other", map[string]any{"url": sec.srv.URL}), "instant", nil, Filter{})
	rules, _ = e.st.ListRules(ctx, e.sc)
	for _, r := range rules {
		if !contains(r.EventTypes, EventSecurityBrief) {
			if err := e.n.SendBriefNow(ctx, e.sc, r.ID); !errors.Is(err, ErrNotABriefRule) {
				t.Errorf("not a brief rule: %v", err)
			}
		}
	}
}

func TestOverdueLine(t *testing.T) {
	now := time.Date(2026, 10, 22, 9, 0, 0, 0, time.UTC)
	us := []versions.Update{
		{Service: store.Service{Name: "php"}, Environment: store.Environment{Name: "prod"}, Due: time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC), DueFor: "exploited vulnerability"},
		{Service: store.Service{Name: "nginx"}, Environment: store.Environment{Name: "prod"}, Due: time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC), DueFor: "security fix"},
		{Service: store.Service{Name: "redis"}, Environment: store.Environment{Name: "prod"}, Due: time.Date(2026, 10, 30, 0, 0, 0, 0, time.UTC), DueFor: "end of life"},
		{Service: store.Service{Name: "acked"}, Environment: store.Environment{Name: "prod"}, Due: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC), Acked: true},
	}
	want := "2 upgrades past their remediation deadline: php in prod (exploited vulnerability, 12 days over); nginx in prod (security fix, 2 days over)."
	if got := overdueLine(us, now); got != want {
		t.Errorf("overdueLine = %q", got)
	}
	if got := overdueLine(us[2:], now); got != "" {
		t.Errorf("nothing overdue: %q", got)
	}
}
