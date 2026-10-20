// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package gitops

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestProposing(t *testing.T) {
	pulls := []Pull{
		{URL: "1", Title: "checkout 2.4.0 in prod", Branch: "goliash/checkout-2.4.0-prod"},
		{URL: "2", Title: "Update ghcr.io/acme/web Docker tag to v2.4.0", Branch: "renovate/web"},
		{URL: "3", Title: "Bump ghcr.io/acme/checkout from 2.3.1 to 2.4.10", Branch: "dependabot/x"},
		{URL: "4", Title: "Update ghcr.io/acme/checkout Docker tag to v2.4.0", Branch: "renovate/checkout-2.x"},
	}
	if p, ok := Proposing(pulls, "ghcr.io/acme/checkout", "2.4.0"); !ok || p.URL != "4" {
		t.Fatalf("got %v %v", p, ok)
	}
	if p, ok := Proposing(pulls, "ghcr.io/acme/checkout", "2.4.10"); !ok || p.URL != "3" {
		t.Fatalf("got %v %v", p, ok)
	}
	if _, ok := Proposing(pulls, "ghcr.io/acme/checkout", "2.4"); ok {
		t.Fatal("2.4 matched 2.4.0")
	}
	if _, ok := Proposing(pulls[:1], "ghcr.io/acme/checkout", "2.4.0"); ok {
		t.Fatal("Goliash's own pull request counted")
	}
}

func TestGitHubOpenPulls(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/acme/deploy/pulls" || r.URL.Query().Get("state") != "open" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(`[{"html_url":"https://github.com/acme/deploy/pull/5","title":"Bump api from 1.4.2 to 1.5.0","head":{"ref":"dependabot/api"}}]`))
	}))
	t.Cleanup(srv.Close)
	g := NewGitHub("tok", srv.URL)
	g.host = "github.com"
	pulls, err := g.OpenPulls(context.Background(), "https://github.com/acme/deploy")
	if err != nil || len(pulls) != 1 || pulls[0].Branch != "dependabot/api" {
		t.Fatalf("OpenPulls = %v, %v", pulls, err)
	}
	if p, ok := Proposing(pulls, "ghcr.io/acme/api", "1.5.0"); !ok || p.URL != "https://github.com/acme/deploy/pull/5" {
		t.Fatal("Dependabot's pull request not found")
	}
}

func TestDeployFiles(t *testing.T) {
	for _, c := range []struct {
		file, dir string
		want      bool
	}{
		{"nomad/vault.nomad.hcl", "nomad/vault.nomad.hcl", true},
		{"nomad/vault.nomad.hcl", "nomad", true},
		{"nomad/vault.nomad.hcl", "", true},
		{"nomad-old/vault.hcl", "nomad", false},
		{"README.md", "", false},
		{"compose/tools/compose.yaml", "compose/tools", true},
		{"ecs/api/task-definition.json", "ecs/api", true},
	} {
		if got := deployFile(c.file) && underPath(c.file, c.dir); got != c.want {
			t.Errorf("%s under %q: %v", c.file, c.dir, got)
		}
	}
}

func TestGitHubPullChecks(t *testing.T) {
	for _, c := range []struct {
		runs, status, want string
	}{
		{`[{"status":"completed","conclusion":"success"}]`, `{"state":"success","statuses":[{}]}`, "passing"},
		{`[{"status":"completed","conclusion":"success"},{"status":"in_progress","conclusion":null}]`, `{"state":"pending","statuses":[]}`, "pending"},
		{`[{"status":"completed","conclusion":"failure"},{"status":"in_progress","conclusion":null}]`, `{"state":"","statuses":[]}`, "failing"},
		{`[]`, `{"state":"failure","statuses":[{}]}`, "failing"},
		{`[]`, `{"state":"pending","statuses":[]}`, ""},
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/repos/acme/deploy/pulls/5":
				_, _ = w.Write([]byte(`{"head":{"sha":"abc"}}`))
			case "/repos/acme/deploy/commits/abc/check-runs":
				_, _ = w.Write([]byte(`{"check_runs":` + c.runs + `}`))
			case "/repos/acme/deploy/commits/abc/status":
				_, _ = w.Write([]byte(c.status))
			default:
				w.WriteHeader(http.StatusNotFound)
			}
		}))
		g := NewGitHub("tok", srv.URL)
		g.host = "github.com"
		got, err := g.PullChecks(context.Background(), "https://github.com/acme/deploy/pull/5")
		srv.Close()
		if err != nil || got != c.want {
			t.Errorf("%s %s: %q, %v; want %q", c.runs, c.status, got, err, c.want)
		}
	}
}
