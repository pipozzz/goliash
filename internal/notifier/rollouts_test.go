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

func TestRolloutAlerts(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	slack := newSink(t)
	e.rule(e.channel("slack", "ops", map[string]any{"url": slack.srv.URL}), "instant", []string{EventRollout}, Filter{})
	tgt, err := e.st.CreateTarget(ctx, store.Target{Scope: e.sc, EnvironmentID: e.prod.ID, Platform: "kubernetes", Name: "k8s-prod"})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	e.clock = now
	three := 3
	put := func(workload, tag string, running int, at time.Time) {
		in := store.Instance{
			TargetID: tgt.ID, EnvironmentID: e.prod.ID, ServiceID: e.svc.ID, WorkloadID: workload, WorkloadName: workload,
			ContainerName: "api", Image: "ghcr.io/acme/api:" + tag, Tag: tag, Digest: "sha256:" + tag, Running: running, Desired: &three, IsMain: true,
		}
		if err := e.st.ApplySnapshot(ctx, store.SnapshotChanges{Scope: e.sc, SnapshotID: store.NewID(), TargetID: tgt.ID, At: at, Upsert: []store.Instance{in}}); err != nil {
			t.Fatal(err)
		}
	}
	put("api", "1.5.0", 2, now.Add(-48*time.Hour))
	put("api", "1.6.0", 1, now.Add(-40*time.Minute))
	put("worker", "1.5.0", 0, now.Add(-2*time.Hour))
	for range 2 { // once each
		if err := e.n.CheckRollouts(ctx); err != nil {
			t.Fatal(err)
		}
		_ = e.n.DeliverDue(ctx)
	}
	all := strings.Join(slack.all(), "\n")
	for _, want := range []string{
		"rollout stuck on k8s-prod (api): 1.6.0 on 1 of 3, 1.5.0 on 2",
		"none of 3 replicas running on k8s-prod (worker)",
	} {
		if !strings.Contains(all, want) {
			t.Errorf("alerts miss %q:\n%s", want, all)
		}
	}
	if slack.count() != 1 || strings.Contains(all, "→") {
		t.Errorf("want one message without a version arrow, got %d:\n%s", slack.count(), all)
	}
}
