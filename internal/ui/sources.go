// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package ui

import (
	"encoding/json"
	"errors"
	"net/http"
	"path"
	"slices"
	"strings"

	"github.com/pipozzz/goliash/internal/auth"
	"github.com/pipozzz/goliash/internal/store"
)

// SourceRow is one thing an agent can watch, on its page: what it is, whether it watches it, and how to set it up.
type SourceRow struct {
	Key, Label, Detail string
	State              string // watching, found (it found it: add it below) or off
	Always             bool   // where the agent runs: it watches it by itself
}

var awsPlatforms = []string{"ecs", "lambda", "rds", "elasticache", "eks", "opensearch", "msk"}

// agentRuns is how an agent runs, from what it collects: docker, kubernetes, nomad, ecs or other.
func agentRuns(platforms []string) string {
	for _, want := range []string{"kubernetes", "nomad", "docker", "compose", "swarm", "ecs"} {
		if slices.Contains(platforms, want) {
			switch want {
			case "compose", "swarm":
				return "docker"
			}
			return want
		}
	}
	return "other"
}

// agentSources lists what an agent can watch, given how it runs, what it collects and what it found.
func agentSources(runs string, collects []string, found []FoundView) []SourceRow {
	foundOn := func(ps ...string) bool {
		return slices.ContainsFunc(found, func(f FoundView) bool { return slices.Contains(ps, f.Platform) })
	}
	row := func(key, label string, platforms ...string) SourceRow {
		r := SourceRow{Key: key, Label: label, State: "off"}
		var on []string
		for _, p := range platforms {
			if slices.Contains(collects, p) {
				on = append(on, p)
			}
		}
		switch {
		case len(on) > 0:
			r.State, r.Detail = "watching", strings.Join(on, ", ")
		case foundOn(platforms...):
			r.State = "found"
		}
		return r
	}
	var out []SourceRow
	switch runs {
	case "docker":
		r := row("self", "Containers and Compose projects on this host", "docker", "swarm")
		r.Always = true
		out = append(out, r, row("host", "This host's operating system and packages", "host"), row("compose", "Compose files on disk", "compose"))
	case "kubernetes":
		r := row("self", "Workloads in this cluster", "kubernetes")
		r.Always = true
		out = append(out, r)
	case "nomad":
		r := row("self", "Jobs of this Nomad cluster", "nomad")
		r.Always = true
		out = append(out, r, row("host", "This host's operating system and packages", "host"))
	case "ecs":
		r := row("self", "ECS clusters of this account", "ecs")
		r.Always = true
		out = append(out, r)
	default:
		out = append(out, row("host", "This host's operating system and packages", "host"))
	}
	aws := row("aws", "AWS: ECS, Lambda, RDS, ElastiCache, EKS, OpenSearch, MSK", awsPlatforms...)
	if runs == "ecs" {
		aws.Label = "Other AWS services: Lambda, RDS, ElastiCache, EKS, OpenSearch, MSK"
	}
	out = append(out, aws)
	if runs != "nomad" && runs != "ecs" {
		out = append(out, row("nomad", "A Nomad cluster", "nomad"))
	}
	return out
}

// AddSourceView is the page that sets up one more thing for an agent to watch: the lines for its configuration.
type AddSourceView struct {
	Base
	Agent    AgentView
	What     string // host, compose or nomad
	Title    string
	Runs     string
	Form     map[string]string
	Config   string
	Restart  string
	Enrolled bool
}

func (s *Server) addSource(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	ctx := r.Context()
	what := r.PathValue("what")
	if what == "aws" {
		return s.addAWS(w, r, p)
	}
	a, err := s.store.GetAgent(ctx, p.Scope, r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		return back(w, r, "/agents", "error", "Unknown agent.")
	} else if err != nil {
		return err
	}
	targets, err := s.store.ListAgentTargets(ctx, p.Scope, a.ID)
	if err != nil {
		return err
	}
	var platforms []string
	for _, t := range targets {
		platforms = append(platforms, t.Platform)
	}
	q := r.URL.Query()
	v := AddSourceView{
		Base: s.base(ctx, p, "agents", a.Name), Agent: agentView(a), What: what, Runs: agentRuns(platforms),
		Form: map[string]string{}, Enrolled: a.CodeID != "",
	}
	var env map[string]string
	var volumes []string
	switch what {
	case "host":
		v.Title = "Watch this host's operating system and packages"
		env = map[string]string{"GOLIASH_HOST_ROOT": "/host"}
		volumes = []string{"/:/host:ro"}
	case "compose":
		v.Title = "Watch Compose files on disk"
		file := strings.TrimSpace(q.Get("file"))
		if file == "" || !path.IsAbs(file) {
			file = "/srv/shop/compose.yaml"
		}
		v.Form["file"] = file
		dir := path.Dir(file)
		project := path.Base(dir)
		declared, _ := json.Marshal([]map[string]any{{"platform": "compose", "name": project, "compose": map[string]any{"files": []string{file}}}})
		env = map[string]string{"GOLIASH_COMPOSE_DIRS": dir, "GOLIASH_TARGETS": string(declared)}
		volumes = []string{dir + ":" + dir + ":ro"}
	case "nomad":
		v.Title = "Watch a Nomad cluster"
		addr := strings.TrimSpace(q.Get("address"))
		if addr == "" {
			addr = "https://nomad.example.com:4646"
		}
		v.Form["address"] = addr
		env = map[string]string{"NOMAD_ADDR": addr, "GOLIASH_CREDENTIAL_NOMAD": "<an ACL token that may list-jobs and read-job>"} //nolint:gosec // a placeholder people replace, not a credential
	default:
		return back(w, r, "/agents/"+a.ID, "error", "Unknown thing to watch.")
	}
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	switch v.Runs {
	case "docker":
		var b strings.Builder
		b.WriteString("# goliash-agent.yml, under services → agent → environment:\n")
		for _, k := range keys {
			b.WriteString("      " + k + ": '" + env[k] + "'\n")
		}
		if len(volumes) > 0 {
			b.WriteString("# and under services → agent → volumes:\n")
			for _, vol := range volumes {
				b.WriteString("      - " + vol + "\n")
			}
		}
		v.Config, v.Restart = b.String(), "docker compose -f goliash-agent.yml up -d"
	case "kubernetes":
		var b strings.Builder
		b.WriteString("# values for the goliash-agent chart:\nenv:\n")
		for _, k := range keys {
			b.WriteString("  " + k + ": '" + env[k] + "'\n")
		}
		v.Config, v.Restart = b.String(), "helm upgrade goliash-agent oci://ghcr.io/pipozzz/charts/goliash-agent -n goliash --reuse-values -f values.yaml"
	default:
		var b strings.Builder
		b.WriteString("# the agent's environment:\n")
		for _, k := range keys {
			b.WriteString(k + "='" + env[k] + "'\n")
		}
		for _, vol := range volumes {
			b.WriteString("# and mount " + vol + "\n")
		}
		v.Config, v.Restart = b.String(), "# restart the agent"
	}
	return render(w, r, AddSourcePage(v))
}
