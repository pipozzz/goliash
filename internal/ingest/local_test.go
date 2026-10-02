// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package ingest

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/pipozzz/goliash/internal/collectors"
	"github.com/pipozzz/goliash/internal/store"
	"github.com/pipozzz/goliash/pkg/agentproto"
)

type staticCollector struct{}

func (staticCollector) Collect(context.Context) (collectors.Result, error) {
	ns := "shop"
	return collectors.Result{Complete: true, Workloads: []agentproto.Workload{{
		ID: "svc-1", Kind: agentproto.SwarmService, Namespace: &ns, Name: "shop_web",
		Labels:     map[string]string{"goliash.service": "web"},
		Containers: []agentproto.Container{{Name: "web", Image: "nginx:1.27.3", Running: 2}},
	}}}, nil
}

func TestServerCollectors(t *testing.T) {
	w := newWorld(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	local, err := w.st.CreateTarget(ctx, store.Target{
		Scope: w.ws.Scope(), EnvironmentID: w.staging.ID, Platform: "swarm",
		Name: "swarm-local", Settings: json.RawMessage(`{"swarm":{"docker_host":"tcp://proxy:2375"}}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	broken, _ := w.st.CreateTarget(ctx, store.Target{
		Scope: w.ws.Scope(), EnvironmentID: w.prod.ID, Platform: "nomad",
		Name: "nomad-local", Settings: json.RawMessage(`{"nomad":{"address":"http://n:4646"}}`),
	})

	m := NewServerCollectors(w.svc, w.st, map[agentproto.Platform]collectors.Factory{
		agentproto.Swarm: func(context.Context, agentproto.Target) (collectors.Collector, error) { return staticCollector{}, nil },
		agentproto.Nomad: func(context.Context, agentproto.Target) (collectors.Collector, error) {
			return nil, errors.New("nomad: permission denied")
		},
	}, w.svc.log)
	go w.svc.RunProcessor(ctx, 50*time.Millisecond)
	go m.Run(ctx, 50*time.Millisecond)

	deadline := time.Now().Add(5 * time.Second)
	for {
		active, _ := w.st.ListActiveInstances(ctx, w.ws.Scope())
		tl, _ := w.st.GetTarget(ctx, w.ws.Scope(), local.ID)
		tb, _ := w.st.GetTarget(ctx, w.ws.Scope(), broken.ID)
		if len(active) == 1 && tl.CollectorStatus == "ok" && tb.CollectorStatus == "failing" {
			if active[0].Tag != "1.27.3" || active[0].ServiceID == "" || active[0].TargetID != local.ID {
				t.Fatalf("instance %+v", active[0])
			}
			if tb.CollectorError != "nomad: permission denied" {
				t.Fatalf("error %q", tb.CollectorError)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("server did not collect: %d instances, statuses %q %q", len(active), tl.CollectorStatus, tb.CollectorStatus)
		}
		time.Sleep(20 * time.Millisecond)
	}

	// The agent target is not collected by the server.
	if snaps, _ := w.st.UnprocessedSnapshots(ctx, 10); len(snaps) > 0 && snaps[0].TargetID == w.target.ID {
		t.Fatal("server collected an agent's target")
	}
}
