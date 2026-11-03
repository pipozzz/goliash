// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package versions

import (
	"encoding/json"
	"sort"
	"strings"

	"github.com/pipozzz/goliash/internal/store"
)

// Where a target is (cloud, account, region, instance, os …), to show and filter by. A target its agent runs on
// or beside (a Docker host, a Kubernetes cluster, a server) is where the agent is. One read through a cloud's API
// (ECS, RDS, Lambda …) shares only the agent's cloud and account, and has its own region.

// apiPlatforms are the platforms an agent reads through AWS's API, wherever the agent runs.
var apiPlatforms = map[string]bool{"ecs": true, "lambda": true, "rds": true, "elasticache": true, "eks": true, "opensearch": true, "msk": true}

// TargetWhere is where target t is, given where its agent runs (nil for the server's own targets).
func TargetWhere(t store.Target, agent map[string]string) map[string]string {
	out := map[string]string{"platform": t.Platform}
	for k, v := range agent {
		if !apiPlatforms[t.Platform] || k == "cloud" || k == "account" {
			out[k] = v
		}
	}
	if apiPlatforms[t.Platform] {
		out["cloud"] = "aws"
	}
	// The settings name the region, and ARNs the account: {"ecs": {"region": "eu-west-1", "clusters": ["arn:…"]}}.
	var settings map[string]json.RawMessage
	_ = json.Unmarshal(t.Settings, &settings)
	for _, raw := range settings {
		var s struct {
			Region   string   `json:"region"`
			Clusters []string `json:"clusters"`
		}
		if json.Unmarshal(raw, &s) != nil {
			continue
		}
		if s.Region != "" {
			out["region"] = s.Region
		}
		for _, c := range s.Clusters {
			if parts := strings.SplitN(c, ":", 6); len(parts) == 6 && parts[0] == "arn" && parts[4] != "" {
				out["account"] = parts[4]
			}
		}
	}
	return out
}

// WhereTokens are where as filter words: region:eu-west-1 account:123456789012 …, sorted.
func WhereTokens(where map[string]string) []string {
	out := make([]string, 0, len(where))
	for k, v := range where {
		if v != "" {
			out = append(out, k+":"+strings.ToLower(v))
		}
	}
	sort.Strings(out)
	return out
}

// WhereIndex is where each service runs in each environment, as filter words (WhereTokens of its targets there),
// keyed by service ID + "|" + environment ID.
func (o Overview) WhereIndex() map[string][]string {
	out := map[string][]string{}
	for _, row := range o.Matrix.Rows {
		for ei, c := range row.Cells {
			if ei >= len(o.Matrix.Environments) {
				break
			}
			key := row.Service.ID + "|" + o.Matrix.Environments[ei].ID
			seen := map[string]bool{}
			for _, v := range c.Versions {
				for _, id := range v.TargetIDs {
					for _, t := range WhereTokens(o.Where[id]) {
						if !seen[t] {
							seen[t] = true
							out[key] = append(out[key], t)
						}
					}
				}
			}
		}
	}
	return out
}

// whereKeys are the keys a filter word names a place by (region:eu-west-1).
var whereKeys = map[string]bool{
	"cloud": true, "account": true, "region": true, "zone": true, "cluster": true, "instance": true,
	"instance_type": true, "platform": true, "os": true, "kernel": true, "arch": true,
}

// SplitWhere splits a filter into its words about where (region:eu-west-1, account:…), lowercased, and the rest.
func SplitWhere(q string) (where []string, rest string) {
	var others []string
	for _, w := range strings.Fields(q) {
		if k, v, ok := strings.Cut(w, ":"); ok && v != "" && whereKeys[strings.ToLower(k)] {
			where = append(where, strings.ToLower(w))
			continue
		}
		others = append(others, w)
	}
	return where, strings.Join(others, " ")
}

// WhereMatches reports whether a place's filter words match every where word: region:eu-west matches
// region:eu-west-1.
func WhereMatches(tokens, where []string) bool {
	for _, w := range where {
		found := false
		for _, t := range tokens {
			if strings.HasPrefix(t, w) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}
