// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package ui

import (
	"context"
	"net/http"
	"sort"
	"time"

	"github.com/pipozzz/goliash/internal/auth"
	"github.com/pipozzz/goliash/internal/store"
	"github.com/pipozzz/goliash/internal/versions"
)

// GitOpsPageView is every GitOps application of the workspace on one page, those that need attention first.
type GitOpsPageView struct {
	Base
	Apps                 []GitOpsView
	Total, Trouble       int
	OutOfSync, Unhealthy int
	Only                 bool // only those that need attention
	CanEdit              bool
}

// gitOpsRow is an application as the pages show it: its state, what Git pins when it is out of sync, the pull
// request that would move it to latest (or the one opened for it), and a rollback when it degraded after a change.
func (s *Server) gitOpsRow(ctx context.Context, sc store.Scope, a versions.GitOpsApp, latest string) GitOpsView {
	now := time.Now()
	g := gitOpsView(a, s.argoCDURL, now)
	g.Service = a.Service
	g.To = s.pullRequestTo(a, latest)
	if g.To != "" {
		if pr, ok := s.pullFor(ctx, sc, a, g.To); ok && pr.State != "closed" {
			g.PRURL, g.PRState, g.PRChecks = pr.URL, pr.State, pr.Checks
		} else if other, ok := s.othersPull(a, g.To); ok {
			g.PRURL, g.PRState, g.PRTitle = other.URL, "open", other.Title
		}
	}
	if g.GitPins = s.gitPins(a, now); g.GitPins != "" && g.GitPins == g.To && g.PRURL == "" {
		g.To = "" // Git has it already: it waits for a sync, not a pull request
	}
	if g.Rollback, g.RollbackWhy = s.rollbackFor(ctx, sc, a, now); g.Rollback != "" {
		if pr, ok := s.pullFor(ctx, sc, a, g.Rollback); ok && pr.State != "closed" {
			g.RollbackPRURL = pr.URL
		}
	}
	return g
}

// gitOpsPage lists the workspace's Argo CD applications and Flux Kustomizations across clusters.
func (s *Server) gitOpsPage(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	ctx := r.Context()
	o, err := versions.LoadOverview(ctx, s.store, p.Scope)
	if err != nil {
		return err
	}
	apps, err := versions.GitOpsApps(ctx, s.store, p.Scope, o)
	if err != nil {
		return err
	}
	v := GitOpsPageView{
		Base: s.base(ctx, p, "promotions", "GitOps applications"), Only: r.URL.Query().Get("only") == "attention",
		CanEdit: p.Can(store.RoleMember) || s.demoPulls(),
	}
	envPos := map[string]int{}
	for i, e := range o.Matrix.Environments {
		envPos[e.Name] = i
	}
	now := time.Now()
	for _, a := range apps {
		latest := ""
		if up, ok := o.Upstreams[a.ServiceID]; ok && up.HasLatest {
			latest = up.Latest.Raw
		}
		g := s.gitOpsRow(ctx, p.Scope, a, latest)
		v.Total++
		switch a.Trouble(now) {
		case "":
		case "out of sync":
			v.Trouble++
			v.OutOfSync++
		default:
			v.Trouble++
			v.Unhealthy++
		}
		if v.Only && g.Trouble == "" {
			continue
		}
		v.Apps = append(v.Apps, g)
	}
	rank := func(g GitOpsView) int {
		switch g.Trouble {
		case "degraded", "missing":
			return 0
		case "out of sync":
			return 1
		}
		return 2
	}
	sort.SliceStable(v.Apps, func(i, j int) bool {
		a, b := v.Apps[i], v.Apps[j]
		if rank(a) != rank(b) {
			return rank(a) < rank(b)
		}
		if envPos[a.Environment] != envPos[b.Environment] {
			return envPos[a.Environment] > envPos[b.Environment] // production first
		}
		if a.Service != b.Service {
			return a.Service < b.Service
		}
		return a.Target < b.Target
	})
	return render(w, r, GitOpsPage(v))
}

// gitOpsReturn is where a preview opened from a GitOps panel comes back to: the GitOps page, or the service page.
func gitOpsReturn(all bool) string {
	if all {
		return "/gitops"
	}
	return ""
}

// GitLabelSnippet is how to name a service's place in Git on one platform.
type GitLabelSnippet struct{ Where, Code string }

// gitLabelSnippets are the goliash.git labels a service's workloads could carry, for the platforms it runs on
// that take them (Kubernetes labels cannot hold an address: there Argo CD or Flux tells).
func gitLabelSnippets(service string, platforms []string) []GitLabelSnippet {
	var out []GitLabelSnippet
	for _, p := range platforms {
		switch p {
		case "nomad":
			out = append(out, GitLabelSnippet{"In the Nomad job (job, group or task):", `meta {
  "goliash.git.repo" = "https://github.com/acme/infra"
  "goliash.git.path" = "nomad/` + service + `.nomad.hcl"
}`})
		case "docker", "compose", "swarm":
			out = append(out, GitLabelSnippet{"In the Compose file, on the service:", `labels:
  goliash.git.repo: https://github.com/acme/infra
  goliash.git.path: compose/` + service + `/compose.yaml`})
		case "ecs":
			out = append(out, GitLabelSnippet{"As tags on the ECS service or task definition:", `goliash.git.repo = https://github.com/acme/infra
goliash.git.path = ecs/` + service + `/task-definition.json`})
		}
	}
	return out
}
