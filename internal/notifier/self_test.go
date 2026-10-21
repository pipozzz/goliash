// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package notifier

import (
	"context"
	"strings"
	"testing"
)

func TestGoliashReleaseAnnouncedOnce(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	slack := newSink(t)
	e.rule(e.channel("slack", "ops", map[string]any{"url": slack.srv.URL}), "instant", []string{EventGoliashRelease}, Filter{})
	latest := ""
	e.n.SetUpdateCheck("1.74.0", func(context.Context, string) (string, error) { return latest, nil })

	if err := e.n.CheckGoliashRelease(ctx); err != nil { // nothing newer
		t.Fatal(err)
	}
	_ = e.n.DeliverDue(ctx)
	if slack.count() != 0 {
		t.Fatalf("announced with nothing newer: %v", slack.all())
	}
	latest = "1.75.0"
	for range 2 { // once per release
		if err := e.n.CheckGoliashRelease(ctx); err != nil {
			t.Fatal(err)
		}
		_ = e.n.DeliverDue(ctx)
	}
	all := strings.Join(slack.all(), "\n")
	if slack.count() != 1 || !strings.Contains(all, "Goliash 1.75.0 is out (this server runs 1.74.0)") {
		t.Fatalf("want one announcement, got %d:\n%s", slack.count(), all)
	}
}

// A release with security fixes says so, and names them.
func TestGoliashSecurityRelease(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	slack := newSink(t)
	e.rule(e.channel("slack", "ops", map[string]any{"url": slack.srv.URL}), "instant", []string{EventGoliashRelease}, Filter{})
	e.n.SetUpdateCheck("1.110.0", func(context.Context, string) (string, error) { return "1.111.0", nil })
	e.n.SetSecurityCheck(func(_ context.Context, from, to string) []string {
		if from != "1.110.0" || to != "1.111.0" {
			t.Errorf("fixes asked for %s..%s", from, to)
		}
		return []string{"fix(security): build with Go 1.26.10 (GO-2026-6809) (#536)"}
	})
	if err := e.n.CheckGoliashRelease(ctx); err != nil {
		t.Fatal(err)
	}
	_ = e.n.DeliverDue(ctx)
	all := strings.Join(slack.all(), "\n")
	if !strings.Contains(all, "Goliash 1.111.0 is out with security fixes (this server runs 1.110.0)") ||
		!strings.Contains(all, "Security fixes: fix(security): build with Go 1.26.10 (GO-2026-6809) (#536)") ||
		!strings.Contains(all, "Goliash security release") {
		t.Fatalf("announcement:\n%s", all)
	}
}
