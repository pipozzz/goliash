// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package notifier

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/pipozzz/goliash/internal/store"
)

// A rule for one region gets what runs there: a drift in that environment, a release of a service running there.
func TestWhereFilter(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	runs := func(agent string, where map[string]string, env store.Environment, platform string) {
		a, _ := e.st.CreateAgent(ctx, e.sc, agent, agent+"-hash")
		_ = e.st.SetAgentWhere(ctx, e.sc, a.ID, where)
		tg, _ := e.st.CreateTarget(ctx, store.Target{Scope: e.sc, EnvironmentID: env.ID, AgentID: a.ID, Platform: platform, Name: agent})
		snap := store.NewID()
		_, _ = e.st.InsertSnapshot(ctx, store.Snapshot{ID: snap, Scope: e.sc, TargetID: tg.ID, CollectedAt: e.clock, Complete: true, Payload: json.RawMessage(`{}`)})
		if err := e.st.ApplySnapshot(ctx, store.SnapshotChanges{Scope: e.sc, SnapshotID: snap, TargetID: tg.ID, At: e.clock, Upsert: []store.Instance{{
			Scope: e.sc, TargetID: tg.ID, EnvironmentID: env.ID, ServiceID: e.svc.ID, WorkloadID: "w-" + agent, WorkloadKind: "deployment",
			WorkloadName: "payments-api", ContainerName: "main", Image: "ghcr.io/acme/payments:1.5.0", Tag: "1.5.0", Running: 1, IsMain: true,
		}}}); err != nil {
			t.Fatal(err)
		}
	}
	runs("eu", map[string]string{"cloud": "aws", "account": "111111111111", "region": "eu-central-1"}, e.prod, "kubernetes")
	runs("us", map[string]string{"cloud": "aws", "account": "222222222222", "region": "us-east-1"}, e.stg, "kubernetes")

	slack := newSink(t)
	e.rule(e.channel("slack", "eu", map[string]any{"url": slack.srv.URL}), "instant", nil, Filter{Where: []string{"region:eu-central"}})

	e.emit(store.Event{Type: "drift_detected", ServiceID: e.svc.ID, EnvironmentID: e.stg.ID, FromVersion: "1.5.0", ToVersion: "1.6.0", Note: "upstream"})
	e.emit(release(e.db, "15.6", "15.7", "minor")) // runs nowhere
	if slack.count() != 0 {
		t.Fatalf("delivered what runs elsewhere: %s", slack.last())
	}
	e.emit(store.Event{Type: "drift_detected", ServiceID: e.svc.ID, EnvironmentID: e.prod.ID, FromVersion: "1.5.0", ToVersion: "1.6.0", Note: "upstream"})
	e.emit(release(e.svc, "1.5.0", "1.7.0", "minor"))
	if slack.count() != 2 {
		t.Fatalf("delivered %d of the two that run in eu-central-1", slack.count())
	}
}
