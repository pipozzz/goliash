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

func TestGitOpsAlerts(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	slack := newSink(t)
	e.rule(e.channel("slack", "ops", map[string]any{"url": slack.srv.URL}), "instant", []string{EventGitOps}, Filter{})
	tgt, err := e.st.CreateTarget(ctx, store.Target{Scope: e.sc, EnvironmentID: e.prod.ID, Platform: "kubernetes", Name: "k8s-prod"})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	e.clock = now
	if err := e.st.ApplySnapshot(ctx, store.SnapshotChanges{
		Scope: e.sc, SnapshotID: store.NewID(), TargetID: tgt.ID, At: now.Add(-2 * time.Hour),
		Upsert: []store.Instance{{
			TargetID: tgt.ID, EnvironmentID: e.prod.ID, ServiceID: e.svc.ID, WorkloadID: "w1", WorkloadName: "api",
			ContainerName: "api", Image: "ghcr.io/acme/api:1.5.0", Tag: "1.5.0", Running: 1, IsMain: true,
		}},
	}); err != nil {
		t.Fatal(err)
	}
	g := store.WorkloadGitOps{
		WorkloadID: "w1", Tool: "argocd", App: "api-prod", Repo: "https://github.com/acme/deploy.git",
		Path: "apps/api/prod", TargetRevision: "main", Revision: "4f1c2e9", Sync: "OutOfSync", Health: "Healthy",
	}
	set := func(at time.Time) {
		if err := e.st.SetWorkloadGitOps(ctx, e.sc, tgt.ID, []store.WorkloadGitOps{g}, true, at); err != nil {
			t.Fatal(err)
		}
	}
	set(now.Add(-2 * time.Minute)) // just changed: not yet
	if err := e.n.CheckGitOps(ctx); err != nil {
		t.Fatal(err)
	}
	_ = e.n.DeliverDue(ctx)
	if slack.count() != 0 {
		t.Fatalf("alerted within the grace period: %v", slack.all())
	}
	g.Sync = "Synced"
	set(now.Add(-time.Hour)) // in sync: nothing
	g.Sync = "OutOfSync"
	set(now.Add(-30 * time.Minute)) // out of sync for half an hour
	for range 2 {                   // once per episode
		if err := e.n.CheckGitOps(ctx); err != nil {
			t.Fatal(err)
		}
		_ = e.n.DeliverDue(ctx)
	}
	all := strings.Join(slack.all(), "\n")
	if slack.count() != 1 || !strings.Contains(all, "Argo CD application api-prod is out of sync on k8s-prod (apps/api/prod @ main)") ||
		!strings.Contains(all, "https://github.com/acme/deploy/commit/4f1c2e9") {
		t.Fatalf("want one alert, got %d:\n%s", slack.count(), all)
	}
}
