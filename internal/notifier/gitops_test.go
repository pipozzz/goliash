// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package notifier

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/pipozzz/goliash/internal/store"
	"github.com/pipozzz/goliash/internal/versions"
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
	e.n.SetGitPins(func(context.Context, versions.GitOpsApp) string { return "1.6.0" })
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
	if slack.count() != 1 || !strings.Contains(all, "Argo CD application api-prod is out of sync on k8s-prod (apps/api/prod @ main): Git pins 1.6.0, 1.5.0 runs") ||
		!strings.Contains(all, "https://github.com/acme/deploy/commit/4f1c2e9") {
		t.Fatalf("want one alert, got %d:\n%s", slack.count(), all)
	}

	// Degraded an hour after 1.5.0 replaced 1.4.2: the alert says so, the likeliest cause.
	if err := e.st.InsertEvent(ctx, e.sc, store.Event{
		Type: "version_changed", ServiceID: e.svc.ID, EnvironmentID: e.prod.ID, TargetID: tgt.ID,
		FromVersion: "1.4.2", ToVersion: "1.5.0", At: now.Add(-2 * time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	e.n.SetPublicURL("https://goliash.example.com")
	e.n.SetRollback(func(context.Context, store.Scope, versions.GitOpsApp) string { return "1.4.2" })
	g.Sync, g.Health = "Synced", "Degraded"
	set(now.Add(-time.Hour))
	if err := e.n.CheckGitOps(ctx); err != nil {
		t.Fatal(err)
	}
	_ = e.n.DeliverDue(ctx)
	if all := strings.Join(slack.all(), "\n"); slack.count() != 2 || !strings.Contains(all, "is degraded on k8s-prod (apps/api/prod @ main), an hour after 1.5.0 replaced 1.4.2") {
		t.Fatalf("want the degraded alert with its cause, got %d:\n%s", slack.count(), all)
	}
	if all := strings.Join(slack.all(), "\n"); !strings.Contains(all, "Rollback PR → 1.4.2") ||
		!strings.Contains(all, "https://goliash.example.com/services/payments-api/gitops/preview?") {
		t.Fatalf("no rollback button:\n%s", all)
	}
}

func TestPullRequestOpened(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	slack := newSink(t)
	e.rule(e.channel("slack", "ops", map[string]any{"url": slack.srv.URL}), "instant", []string{EventPullRequest}, Filter{})
	e.n.PullRequestOpened(e.sc, Item{
		Service: "checkout", Environment: "dev", Target: "k8s-dev", From: "2.3.1", To: "2.4.0",
		Note: "auto-pr", URL: "https://github.com/acme/deploy/pull/7",
	})
	_ = e.n.DeliverDue(ctx)
	all := strings.Join(slack.all(), "\n")
	if slack.count() != 1 || !strings.Contains(all, "pull request opened to move 2.3.1 to 2.4.0 on k8s-dev, by the version policy (auto_pr)") ||
		!strings.Contains(all, "https://github.com/acme/deploy/pull/7") {
		t.Fatalf("want one announcement, got %d:\n%s", slack.count(), all)
	}
}

// A pull request Goliash opened that has waited for review for more than three days is called out, once.
func TestWaitingPulls(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	slack := newSink(t)
	e.rule(e.channel("slack", "ops", map[string]any{"url": slack.srv.URL}), "instant", []string{EventPullRequest}, Filter{})
	tgt, err := e.st.CreateTarget(ctx, store.Target{Scope: e.sc, EnvironmentID: e.prod.ID, Platform: "kubernetes", Name: "k8s-prod"})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.st.RecordGitOpsPull(ctx, store.GitOpsPull{
		Scope: e.sc, ServiceID: e.svc.ID, Environment: "prod", TargetID: tgt.ID, WorkloadID: "w1",
		From: "1.4.2", To: "1.5.0", URL: "https://github.com/acme/deploy/pull/8", OpenedBy: "auto-pr",
	}); err != nil {
		t.Fatal(err)
	}
	check := func() {
		if err := e.n.CheckWaitingPulls(ctx); err != nil {
			t.Fatal(err)
		}
		_ = e.n.DeliverDue(ctx)
	}
	e.clock = time.Now().Add(2 * 24 * time.Hour)
	check()
	if slack.count() != 0 {
		t.Fatalf("called out after two days: %v", slack.all())
	}
	e.clock = time.Now().Add(4*24*time.Hour + time.Hour)
	check()
	check() // once
	all := strings.Join(slack.all(), "\n")
	if slack.count() != 1 || !strings.Contains(all, "the pull request to move 1.4.2 to 1.5.0 on k8s-prod has waited 4 days for review") ||
		!strings.Contains(all, "https://github.com/acme/deploy/pull/8") {
		t.Fatalf("want one reminder, got %d:\n%s", slack.count(), all)
	}
}
