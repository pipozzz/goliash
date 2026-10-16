// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package versions

import (
	"context"
	"sort"
	"time"

	"github.com/pipozzz/goliash/internal/store"
)

// RolloutKind is what is wrong with a workload's replicas.
type RolloutKind string

// Rollout problems, worst first.
const (
	// RolloutDown is a workload that asks for replicas and runs none.
	RolloutDown RolloutKind = "down"
	// RolloutStuck is a workload running two versions side by side for longer than a rollout takes.
	RolloutStuck RolloutKind = "stuck"
	// RolloutShort is a workload running fewer replicas than it asks for.
	RolloutShort RolloutKind = "short"
)

// rolloutGrace is how long a workload may run two versions, or fewer replicas than it asks for, before it counts:
// a rolling update takes minutes, not half an hour.
const rolloutGrace = 30 * time.Minute

// RolloutVersion is one version a workload runs and on how many replicas.
type RolloutVersion struct {
	Version string
	Running int
	Since   time.Time // since when this version has run there
}

// RolloutIssue is a workload whose replicas are not where they should be.
type RolloutIssue struct {
	Kind                       RolloutKind
	ServiceID, Service         string
	EnvironmentID, Environment string
	TargetID, Target           string
	Workload                   string // namespace/name
	Running, Desired           int
	Versions                   []RolloutVersion // newest first
	Since                      time.Time        // since when it has been so, as far as Goliash can tell
}

// RolloutIssues finds the workloads of active instances whose replicas are not where they should be: none
// running, two versions side by side past the grace period, or fewer than asked for. Only main containers of
// workloads whose platform reports the replicas it asks for count.
func RolloutIssues(ctx context.Context, st *store.Store, sc store.Scope, o Overview, now time.Time) ([]RolloutIssue, error) {
	active, err := st.ListActiveInstances(ctx, sc)
	if err != nil {
		return nil, err
	}
	targetName := map[string]string{}
	for _, t := range o.Targets {
		targetName[t.ID] = t.Name
	}
	type key struct{ target, workload string }
	groups := map[key][]store.Instance{}
	var order []key
	for _, in := range active {
		if !in.IsMain || in.Desired == nil {
			continue
		}
		k := key{in.TargetID, in.WorkloadID}
		if _, ok := groups[k]; !ok {
			order = append(order, k)
		}
		groups[k] = append(groups[k], in)
	}
	var out []RolloutIssue
	for _, k := range order {
		ins := groups[k]
		first := ins[0]
		is := RolloutIssue{
			ServiceID: first.ServiceID, Service: o.Services[first.ServiceID].Name, EnvironmentID: first.EnvironmentID,
			Environment: o.Envs[first.EnvironmentID].Name, TargetID: first.TargetID, Target: targetName[first.TargetID],
			Workload: first.WorkloadName,
		}
		if first.Namespace != "" {
			is.Workload = first.Namespace + "/" + first.WorkloadName
		}
		if is.Service == "" {
			is.Service = first.WorkloadName
		}
		byVersion := map[string]*RolloutVersion{}
		var latest time.Time // the latest change: a version that started running
		for _, in := range ins {
			is.Desired = max(is.Desired, *in.Desired)
			is.Running += in.Running
			if in.FirstSeenAt.After(latest) {
				latest = in.FirstSeenAt
			}
			if in.Running == 0 {
				continue
			}
			v := byVersion[in.Tag]
			if v == nil {
				v = &RolloutVersion{Version: in.Tag, Since: in.FirstSeenAt}
				byVersion[in.Tag] = v
			}
			v.Running += in.Running
			if in.FirstSeenAt.Before(v.Since) {
				v.Since = in.FirstSeenAt
			}
		}
		for _, v := range byVersion {
			is.Versions = append(is.Versions, *v)
		}
		sort.Slice(is.Versions, func(i, j int) bool { return is.Versions[i].Since.After(is.Versions[j].Since) })
		settled := now.Sub(latest) >= rolloutGrace
		switch {
		case is.Desired > 0 && is.Running == 0 && settled:
			is.Kind, is.Since = RolloutDown, latest
		case len(is.Versions) > 1 && settled:
			is.Kind, is.Since = RolloutStuck, is.Versions[0].Since
		case is.Running < is.Desired && settled:
			is.Kind, is.Since = RolloutShort, latest
		default:
			continue
		}
		out = append(out, is)
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Kind != b.Kind {
			return rolloutRank(a.Kind) < rolloutRank(b.Kind)
		}
		if a.Service != b.Service {
			return a.Service < b.Service
		}
		if a.Environment != b.Environment {
			return a.Environment < b.Environment
		}
		return a.Target < b.Target
	})
	return out, nil
}

func rolloutRank(k RolloutKind) int {
	switch k {
	case RolloutDown:
		return 0
	case RolloutStuck:
		return 1
	}
	return 2
}
