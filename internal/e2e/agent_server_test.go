// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package e2e

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/pipozzz/goliash/internal/agent"
	"github.com/pipozzz/goliash/internal/api"
	"github.com/pipozzz/goliash/internal/collectors"
	"github.com/pipozzz/goliash/internal/ingest"
	"github.com/pipozzz/goliash/internal/store"
	"github.com/pipozzz/goliash/internal/tokens"
	"github.com/pipozzz/goliash/pkg/agentproto"
)

type staticCollector struct{}

func (staticCollector) Collect(context.Context) (collectors.Result, error) {
	return collectors.Result{Complete: true, Workloads: []agentproto.Workload{{
		ID: "uid-1", Kind: agentproto.Deployment, Namespace: ptr("payments"), Name: "payments-api",
		Labels:     map[string]string{"app.kubernetes.io/name": "payments-api"},
		Containers: []agentproto.Container{{Name: "app", Image: "ghcr.io/acme/payments-api:1.4.2", Running: 3}},
	}}}, nil
}

func ptr[T any](v T) *T { return &v }

func TestAgentAgainstServer(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	st, err := store.Open(ctx, "sqlite://:memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	ws, err := st.EnsureDefaultWorkspace(ctx)
	if err != nil {
		t.Fatal(err)
	}
	prod, err := st.CreateEnvironment(ctx, ws.Scope(), "prod", 30)
	if err != nil {
		t.Fatal(err)
	}
	token, hash := tokens.New(tokens.Agent)
	ag, err := st.CreateAgent(ctx, ws.Scope(), "eu-cluster", hash)
	if err != nil {
		t.Fatal(err)
	}
	k8s, err := st.CreateTarget(ctx, store.Target{
		Scope: ws.Scope(), EnvironmentID: prod.ID, AgentID: ag.ID,
		Platform: "kubernetes", Name: "prod-eu-1", Settings: json.RawMessage(`{"kubernetes":{}}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	swarm, err := st.CreateTarget(ctx, store.Target{
		Scope: ws.Scope(), EnvironmentID: prod.ID, AgentID: ag.ID,
		Platform: "swarm", Name: "swarm-prod", Settings: json.RawMessage(`{"swarm":{"docker_host":"tcp://proxy:2375"}}`),
	})
	if err != nil {
		t.Fatal(err)
	}

	h, err := api.NewAgentHandler(st, ingest.New(st, log), log)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	h.Register(mux)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	a, err := agent.New(agent.Options{
		ServerURL: srv.URL, Token: token, DataDir: t.TempDir(), Logger: log,
		Collectors: map[agentproto.Platform]collectors.Factory{
			agentproto.Kubernetes: func(context.Context, agentproto.Target) (collectors.Collector, error) {
				return staticCollector{}, nil
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()

	deadline := time.Now().Add(10 * time.Second)
	for {
		snaps, err := st.UnprocessedSnapshots(ctx, 10)
		if err != nil {
			t.Fatal(err)
		}
		sw, _ := st.GetTarget(ctx, ws.Scope(), swarm.ID)
		if len(snaps) == 1 && sw.CollectorStatus != "" {
			if snaps[0].TargetID != k8s.ID || !snaps[0].Complete {
				t.Fatalf("snapshot %+v", snaps[0])
			}
			var payload agentproto.Snapshot
			if err := json.Unmarshal(snaps[0].Payload, &payload); err != nil || payload.Workloads[0].Name != "payments-api" {
				t.Fatalf("payload %s (%v)", snaps[0].Payload, err)
			}
			if sw.CollectorStatus != "failing" || sw.CollectorError == "" {
				t.Fatalf("swarm target status %q %q", sw.CollectorStatus, sw.CollectorError)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("agent did not deliver: %d snapshots, swarm status %q", len(snaps), sw.CollectorStatus)
		}
		time.Sleep(20 * time.Millisecond)
	}

	got, err := st.GetAgent(ctx, ws.Scope(), ag.ID)
	if err != nil || got.RegisteredAt.IsZero() || got.Version == "" || got.LastSeenAt.IsZero() {
		t.Fatalf("agent record %+v %v", got, err)
	}
	if len(got.Platforms) != 1 || got.Platforms[0] != "kubernetes" {
		t.Fatalf("platforms %v", got.Platforms)
	}

	cancel()
	if err := <-done; err != nil {
		t.Fatalf("agent Run: %v", err)
	}
}
