// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package mapping

import (
	"testing"

	"github.com/pipozzz/goliash/internal/store"
	"github.com/pipozzz/goliash/internal/versions"
)

func TestMapLayers(t *testing.T) {
	m, errs := New([]store.MappingRule{
		{ID: "r1", Priority: 10, MatchType: "image_repo", Pattern: `ghcr\.io/acme/pay.*`, ServiceID: "svc-payments"},
		{ID: "r2", Priority: 20, MatchType: "workload_name", Pattern: `billing-.*`, ServiceID: "svc-billing"},
		{ID: "r3", Priority: 30, MatchType: "label", Pattern: `team=risk|fraud`, ServiceID: "svc-risk"},
		{ID: "r4", Priority: 0, MatchType: "ignore", Pattern: `docker\.io/datadog/agent`},
		{ID: "bad", Priority: 1, MatchType: "workload_name", Pattern: `(`},
		{ID: "bad2", Priority: 1, MatchType: "label", Pattern: `no-equals`},
	})
	if len(errs) != 2 {
		t.Fatalf("errs = %v", errs)
	}

	img := versions.ParseImage
	cases := []struct {
		name      string
		w         Workload
		container string
		image     string
		want      Decision
	}{
		{
			"goliash label wins over rules",
			Workload{Name: "x", Labels: map[string]string{"goliash.service": "checkout", "goliash.env": "staging"}},
			"app", "ghcr.io/acme/payments:1",
			Decision{ServiceName: "checkout", EnvName: "staging", Source: "label"},
		},
		{
			"kubernetes name label",
			Workload{Name: "x", Labels: map[string]string{"app.kubernetes.io/name": "web"}},
			"app", "nginx:1.27",
			Decision{ServiceName: "web", Source: "label"},
		},
		{
			"image rule",
			Workload{Name: "pay-v2"},
			"app", "ghcr.io/acme/payments-api:1.4.2",
			Decision{ServiceID: "svc-payments", Source: "rule"},
		},
		{
			"workload rule",
			Workload{Name: "billing-prod"},
			"app", "ghcr.io/acme/invoicer:3",
			Decision{ServiceID: "svc-billing", Source: "rule"},
		},
		{
			"label rule",
			Workload{Name: "scorer", Labels: map[string]string{"team": "fraud"}},
			"app", "ghcr.io/acme/scorer:1",
			Decision{ServiceID: "svc-risk", Source: "rule"},
		},
		{
			"ignore rule",
			Workload{Name: "pay", Labels: map[string]string{"goliash.service": "pay"}},
			"dd", "datadog/agent:7",
			Decision{Ignore: true, Source: "rule"},
		},
		{
			"heuristic",
			Workload{Name: "foo-prod-v2"},
			"app", "ghcr.io/org/foo:1.2.3",
			Decision{Suggested: "foo", Source: "heuristic"},
		},
	}
	for _, c := range cases {
		if got := m.Map(c.w, c.container, img(c.image)); got != c.want {
			t.Errorf("%s:\n got %+v\nwant %+v", c.name, got, c.want)
		}
	}
}

func TestMainContainer(t *testing.T) {
	cases := []struct {
		name       string
		w          Workload
		containers []string
		service    string
		want       string
	}{
		{"single", Workload{Name: "api"}, []string{"app"}, "", "app"},
		{"sidecars skipped", Workload{Name: "api"}, []string{"envoy", "istio-proxy", "server"}, "", "server"},
		{"named like workload", Workload{Name: "api"}, []string{"api", "worker"}, "", "api"},
		{"named like service", Workload{Name: "api-v2"}, []string{"payments", "worker"}, "payments", "payments"},
		{"label", Workload{Name: "api", Labels: map[string]string{"goliash.container": "worker"}}, []string{"api", "worker"}, "", "worker"},
		{"nomad group/task", Workload{Name: "payments"}, []string{"web/proxy", "web/payments"}, "", "web/payments"},
		{"first by name", Workload{Name: "x"}, []string{"b", "a"}, "", "a"},
		{"only sidecars", Workload{Name: "mesh"}, []string{"envoy"}, "", "envoy"},
	}
	for _, c := range cases {
		if got := MainContainer(c.w, c.containers, c.service); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}
