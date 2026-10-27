// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package ui

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/pipozzz/goliash/internal/auth"
	"github.com/pipozzz/goliash/internal/store"
	"github.com/pipozzz/goliash/pkg/agentproto"
)

// FoundView is something an agent with a token found it could collect and does not yet: one click adds it.
type FoundView struct {
	Index          int
	Platform, Name string
	Detail         string // where: the region, the clusters, the Docker host
}

// foundSettings are the settings of a target an agent found, as targets store them: {"ecs": {"region": …}}.
func foundSettings(d agentproto.DeclaredTarget) json.RawMessage {
	b, _ := json.Marshal(d)
	var m map[string]json.RawMessage
	_ = json.Unmarshal(b, &m)
	for _, k := range []string{"platform", "name", "poll_interval_seconds", "debounce_seconds", "credentials_ref"} {
		delete(m, k)
	}
	out, _ := json.Marshal(m)
	return out
}

// settingsKey is what tells two targets of a platform apart, whatever their names: the same settings.
func settingsKey(platform string, settings json.RawMessage) string {
	var m any
	if json.Unmarshal(settings, &m) != nil {
		return platform
	}
	b, _ := json.Marshal(m) // object keys come out sorted
	return platform + "|" + string(b)
}

// foundDetail says where a found target is.
func foundDetail(settings json.RawMessage) string {
	var m map[string]map[string]any
	_ = json.Unmarshal(settings, &m)
	var parts []string
	for _, v := range m {
		for _, k := range []string{"region", "clusters", "docker_host", "kubeconfig_context", "address", "root"} {
			switch x := v[k].(type) {
			case string:
				if x != "" {
					parts = append(parts, x)
				}
			case []any:
				for _, c := range x {
					if s, ok := c.(string); ok {
						parts = append(parts, s)
					}
				}
			}
		}
	}
	return strings.Join(parts, " · ")
}

// foundOf lists what an agent found and does not collect yet.
func (s *Server) foundOf(ctx context.Context, sc store.Scope, a store.Agent) ([]FoundView, error) {
	if len(a.Discovered) == 0 {
		return nil, nil
	}
	have, err := s.store.ListAgentTargets(ctx, sc, a.ID)
	if err != nil {
		return nil, err
	}
	known := map[string]bool{}
	for _, t := range have {
		known[settingsKey(t.Platform, t.Settings)] = true
		known[t.Platform+":"+t.Name] = true
	}
	var out []FoundView
	for i, d := range a.Discovered {
		set := foundSettings(d)
		if known[settingsKey(string(d.Platform), set)] || known[string(d.Platform)+":"+d.Name] {
			continue
		}
		out = append(out, FoundView{Index: i, Platform: string(d.Platform), Name: d.Name, Detail: foundDetail(set)})
	}
	return out, nil
}

// addFound adds what an agent found, the ticked ones, in one environment.
func (s *Server) addFound(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	ctx := r.Context()
	page := "/agents/" + r.PathValue("id")
	a, err := s.store.GetAgent(ctx, p.Scope, r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		return back(w, r, "/agents", "error", "Unknown agent.")
	} else if err != nil {
		return err
	}
	env, err := s.store.GetEnvironmentByName(ctx, p.Scope, r.FormValue("environment"))
	if err != nil {
		return back(w, r, page, "error", "Pick an environment.")
	}
	_ = r.ParseForm()
	var added []string
	for _, raw := range r.Form["found"] {
		i, err := strconv.Atoi(raw)
		if err != nil || i < 0 || i >= len(a.Discovered) {
			continue
		}
		d := a.Discovered[i]
		if !d.Platform.Valid() || slices.Contains(added, d.Name) {
			continue
		}
		t := store.Target{Scope: p.Scope, EnvironmentID: env.ID, AgentID: a.ID, Platform: string(d.Platform), Settings: foundSettings(d)}
		for n := 1; n < 20; n++ {
			t.Name = d.Name
			if n > 1 {
				t.Name += "-" + strconv.Itoa(n)
			}
			if _, err = s.store.CreateTarget(ctx, t); err == nil {
				break
			}
		}
		if err != nil {
			return back(w, r, page, "error", "Could not add "+d.Name+": "+err.Error())
		}
		added = append(added, t.Name)
		s.audit(ctx, p, "target.create", "target", t.Name, "platform", t.Platform, "environment", env.Name, "agent", a.Name)
	}
	if len(added) == 0 {
		return back(w, r, page, "error", "Tick what to add.")
	}
	return back(w, r, page, "notice", "Added "+strings.Join(added, ", ")+" in "+env.Name+". The agent starts collecting within a minute.")
}

// noteHint says how to fix what an agent could not use: missing AWS credentials or permissions, mostly.
func noteHint(note string) string {
	platform, _, _ := strings.Cut(note, " ")
	platform = strings.TrimSuffix(platform, ":")
	low := strings.ToLower(note)
	switch {
	case strings.Contains(low, "no ec2 imds role") || strings.Contains(low, "failed to refresh cached credentials") ||
		strings.Contains(low, "no valid providers") || strings.Contains(low, "get identity") || strings.Contains(low, "invalidclienttokenid"):
		return "The agent has no AWS credentials it can use: see Add AWS to this agent."
	case strings.Contains(low, "accessdenied") || strings.Contains(low, "not authorized") || strings.Contains(low, "unauthorizedoperation"):
		if perms := awsPermissions[platform]; perms != "" {
			return "Its AWS credentials may not read " + platform + ": allow " + perms + " (Add AWS to this agent has the policy)."
		}
		return "Its credentials may not read this; see Add AWS to this agent for the permissions."
	}
	return ""
}

// awsPermissions are the read-only IAM actions each AWS platform needs.
var awsPermissions = map[string]string{
	"ecs":         "ecs:List*, ecs:Describe*",
	"lambda":      "lambda:ListFunctions, lambda:GetFunction, lambda:ListAliases, lambda:ListFunctionUrlConfigs",
	"rds":         "rds:DescribeDBInstances, rds:DescribeDBClusters, rds:DescribeDBEngineVersions, rds:DescribePendingMaintenanceActions",
	"elasticache": "elasticache:Describe*, elasticache:ListTagsForResource",
	"eks":         "eks:ListClusters, eks:DescribeCluster, eks:ListNodegroups, eks:DescribeNodegroup, eks:DescribeClusterVersions",
	"opensearch":  "es:ListDomainNames, es:DescribeDomains, es:ListTags, es:ListVersions",
	"msk":         "kafka:ListClustersV2, kafka:ListKafkaVersions",
}
