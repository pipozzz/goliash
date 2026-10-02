// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

// Package demo fills a workspace with three weeks of realistic example data, so a
// new installation shows a populated matrix, history and drift right away. All data
// goes through the real ingest pipeline; targets and the agent are named demo-*.
package demo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/pipozzz/goliash/internal/ingest"
	"github.com/pipozzz/goliash/internal/registry"
	"github.com/pipozzz/goliash/internal/store"
	"github.com/pipozzz/goliash/internal/tokens"
	"github.com/pipozzz/goliash/internal/versions"
	"github.com/pipozzz/goliash/pkg/agentproto"
)

// ErrAlreadySeeded means the workspace already has demo data.
var ErrAlreadySeeded = errors.New("demo data already present")

// state is what each demo target runs from a given day on.
type step struct {
	day     int // days after the start
	running map[string]string
}

type demoTarget struct {
	name, env, platform string
	steps               []step
}

// Upstream tags offered by the fake registry, before and after the "latest" check.
var tagsBefore = map[string][]string{
	"ghcr.io/acme/payments-api":                {"1.4.2", "1.5.0", "1.6.0"},
	"ghcr.io/acme/checkout":                    {"2.3.1", "2.4.0"},
	"docker.io/library/postgres":               {"15.5", "15.6", "15.7", "16.3", "16.4"},
	"docker.io/library/redis":                  {"7.2.4", "7.2.5", "7.4.0"},
	"quay.io/keycloak/keycloak":                {"24.0.5", "25.0.1", "25.0.6"},
	"docker.io/library/traefik":                {"v3.0.4", "v3.1.2", "v3.1.6"},
	"docker.io/grafana/grafana":                {"11.1.0", "11.2.0"},
	"registry.k8s.io/ingress-nginx/controller": {"v1.11.2"},
}

var tagsAfter = map[string][]string{
	"ghcr.io/acme/payments-api":  {"1.7.0"},
	"docker.io/library/postgres": {"15.8", "17.0"},
	"quay.io/keycloak/keycloak":  {"26.0.0"},
	"docker.io/library/traefik":  {"v3.2.0"},
}

var images = map[string]string{
	"payments-api": "ghcr.io/acme/payments-api",
	"checkout":     "ghcr.io/acme/checkout",
	"postgres":     "postgres",
	"redis":        "redis",
	"keycloak":     "quay.io/keycloak/keycloak",
	"traefik":      "traefik",
	"grafana":      "grafana/grafana",
}

var targets = []demoTarget{
	{name: "demo-dev", env: "dev", platform: "swarm", steps: []step{
		{0, map[string]string{"payments-api": "1.5.0", "checkout": "2.3.1", "postgres": "15.6", "redis": "7.2.4", "keycloak": "24.0.5"}},
		{4, map[string]string{"payments-api": "1.6.0", "checkout": "2.4.0", "postgres": "15.7", "redis": "7.2.5", "keycloak": "25.0.1"}},
		{15, map[string]string{"payments-api": "1.6.0", "checkout": "2.4.0", "postgres": "15.7", "redis": "7.2.5", "keycloak": "25.0.6"}},
	}},
	{name: "demo-staging", env: "staging", platform: "kubernetes", steps: []step{
		{0, map[string]string{"payments-api": "1.4.2", "checkout": "2.3.1", "postgres": "15.6", "redis": "7.2.4", "keycloak": "24.0.5", "traefik": "v3.0.4", "grafana": "11.1.0"}},
		{6, map[string]string{"payments-api": "1.5.0", "checkout": "2.4.0", "postgres": "15.6", "redis": "7.2.4", "keycloak": "24.0.5", "traefik": "v3.1.2", "grafana": "11.2.0"}},
		{17, map[string]string{"payments-api": "1.6.0", "checkout": "2.4.0", "postgres": "15.7", "redis": "7.2.5", "keycloak": "25.0.1", "traefik": "v3.1.2", "grafana": "11.2.0"}},
	}},
	{name: "demo-prod-eu", env: "prod", platform: "kubernetes", steps: []step{
		{0, map[string]string{"payments-api": "1.4.2", "checkout": "2.3.1", "postgres": "15.6", "redis": "7.2.4", "keycloak": "24.0.5", "traefik": "v3.0.4", "grafana": "11.1.0"}},
		{11, map[string]string{"payments-api": "1.5.0", "checkout": "2.3.1", "postgres": "15.6", "redis": "7.2.4", "keycloak": "24.0.5", "traefik": "v3.1.2", "grafana": "11.2.0"}},
	}},
	{name: "demo-prod-us", env: "prod", platform: "ecs", steps: []step{
		{0, map[string]string{"payments-api": "1.4.2", "checkout": "2.3.1"}},
		{13, map[string]string{"payments-api": "1.4.2", "checkout": "2.3.1"}},
	}},
}

type fakeRegistry struct{ tags map[string][]string }

func (f fakeRegistry) ListTags(_ context.Context, repo string, _ registry.Credentials) ([]string, error) {
	if t, ok := f.tags[repo]; ok {
		return t, nil
	}
	return nil, fmt.Errorf("demo registry has no %s", repo)
}

// Seed fills a workspace with demo data. It refuses to run twice.
func Seed(ctx context.Context, st *store.Store, ws store.Workspace, log *slog.Logger) error {
	sc := ws.Scope()
	if _, err := st.GetAgentByName(ctx, sc, "demo-agent"); err == nil {
		return ErrAlreadySeeded
	}

	envIDs := map[string]string{}
	for i, name := range []string{"dev", "staging", "prod"} {
		env, err := st.GetEnvironmentByName(ctx, sc, name)
		if errors.Is(err, store.ErrNotFound) {
			env, err = st.CreateEnvironment(ctx, sc, name, (i+1)*10)
		}
		if err != nil {
			return err
		}
		envIDs[name] = env.ID
	}
	_, hash := tokens.New(tokens.Agent)
	agent, err := st.CreateAgent(ctx, sc, "demo-agent", hash)
	if err != nil {
		return err
	}
	targetIDs := map[string]string{}
	for _, t := range targets {
		// Demo data never refreshes; a 30-day poll interval keeps it from looking stale.
		tg, err := st.CreateTarget(ctx, store.Target{
			Scope: sc, EnvironmentID: envIDs[t.env], AgentID: agent.ID,
			Platform: t.platform, Name: t.name, Settings: json.RawMessage(`{}`), PollIntervalSeconds: 30 * 24 * 3600,
		})
		if err != nil {
			return err
		}
		targetIDs[t.name] = tg.ID
	}

	// Snapshots in time order: every target daily, with its state as of that day.
	svc := ingest.New(st, log)
	start := time.Now().UTC().Add(-21*24*time.Hour - time.Hour)
	for day := 0; day <= 21; day++ {
		for _, t := range targets {
			running := stateOn(t, day)
			snap := agentproto.Snapshot{
				SnapshotID: store.NewID(), TargetID: targetIDs[t.name], Complete: true,
				CollectedAt: start.Add(time.Duration(day)*24*time.Hour + time.Duration(len(t.name))*time.Minute),
			}
			for _, name := range sortedKeys(running) {
				snap.Workloads = append(snap.Workloads, demoWorkload(t, name, running[name]))
			}
			if t.name == "demo-prod-eu" {
				ns := "reports"
				snap.Workloads = append(snap.Workloads, agentproto.Workload{
					ID: "demo-prod-eu/report", Kind: agentproto.Cronjob,
					Namespace: &ns, Name: "nightly-report",
					Containers: []agentproto.Container{{Name: "report", Image: "ghcr.io/acme/report:0.9.1", Running: 0}},
				})
			}
			raw, _ := json.Marshal(snap)
			if _, err := svc.Snapshot(ctx, agent, snap, raw); err != nil {
				return err
			}
		}
		if _, err := svc.ProcessPending(ctx); err != nil {
			return err
		}
	}
	for _, t := range targets {
		if err := st.ReportCollectorStatus(ctx, sc, agent.ID, targetIDs[t.name], "ok", ""); err != nil {
			return err
		}
	}

	// Owners and policies, then two upstream checks: a baseline and one that finds new releases.
	for name, owner := range map[string]string{
		"payments-api": "team-payments", "checkout": "team-shop",
		"postgres": "platform", "redis": "platform", "keycloak": "platform", "traefik": "platform", "grafana": "observability",
	} {
		s, err := st.GetServiceByName(ctx, sc, name)
		if err != nil {
			continue
		}
		s.Owner = owner
		if !strings.HasPrefix(images[name], "ghcr.io/acme/") {
			s.Kind = "third_party"
		}
		switch name {
		case "postgres":
			s.VersionPolicy = json.RawMessage(`{"track":"minor","pin_major":15}`)
		case "keycloak":
			s.VersionPolicy = json.RawMessage(`{"track":"major"}`)
		}
		if err := st.UpdateService(ctx, s); err != nil {
			return err
		}
	}
	reg := fakeRegistry{tags: map[string][]string{}}
	for repo, tags := range tagsBefore {
		reg.tags[repo] = tags
	}
	checker := versions.NewChecker(st, reg, log, 0)
	if err := checker.CheckUpstreams(ctx, sc); err != nil {
		return err
	}
	for repo, tags := range tagsAfter {
		reg.tags[repo] = append(reg.tags[repo], tags...)
	}
	if err := checker.CheckUpstreams(ctx, sc); err != nil {
		return err
	}
	return checker.EvaluateDrift(ctx, sc)
}

func stateOn(t demoTarget, day int) map[string]string {
	var cur map[string]string
	for _, s := range t.steps {
		if s.day <= day {
			cur = s.running
		}
	}
	return cur
}

func demoWorkload(t demoTarget, service, tag string) agentproto.Workload {
	ns := map[string]string{
		"payments-api": "payments", "checkout": "shop", "postgres": "data", "redis": "data",
		"keycloak": "auth", "traefik": "ingress", "grafana": "observability",
	}[service]
	kind := agentproto.Deployment
	switch {
	case t.platform == "ecs":
		kind = agentproto.EcsService
	case t.platform == "swarm":
		kind = agentproto.SwarmService
	case service == "postgres" || service == "redis":
		kind = agentproto.Statefulset
	}
	replicas := map[string]int{"payments-api": 3, "checkout": 2, "postgres": 1, "redis": 1, "keycloak": 2, "traefik": 2, "grafana": 1}[service]
	if t.env != "prod" {
		replicas = 1
	}
	w := agentproto.Workload{
		ID: t.name + "/" + service, Kind: kind, Namespace: &ns, Name: service, DesiredReplicas: &replicas,
		Labels:     map[string]string{"app.kubernetes.io/name": service},
		Containers: []agentproto.Container{{Name: service, Image: images[service] + ":" + tag, Running: replicas}},
	}
	if service == "payments-api" && t.platform == "kubernetes" {
		w.Containers = append(w.Containers, agentproto.Container{Name: "istio-proxy", Image: "docker.io/istio/proxyv2:1.23.2", Running: replicas})
	}
	return w
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
