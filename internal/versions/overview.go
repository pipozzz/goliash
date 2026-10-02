// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package versions

import (
	"context"
	"time"

	"github.com/pipozzz/goliash/internal/store"
)

// Overview is everything the matrix view shows for a workspace.
type Overview struct {
	Matrix    Matrix
	Upstreams map[string]Upstream          // service ID -> newest acceptable release
	Refs      map[string]Reference         // service ID -> upstream repo and reference tag
	Drifts    map[string][]store.Drift     // service ID + "|" + environment ID -> open drifts
	Policies  map[string]Policy            // service ID -> policy
	Services  map[string]store.Service     // by ID
	Envs      map[string]store.Environment // by ID
	Targets   []store.Target
	CheckErrs map[string]string // service ID -> last upstream check error
	// Stale lists targets whose data may be out of date (agent silent, or no recent
	// snapshot), with their last snapshot time.
	Stale map[string]time.Time
}

// StaleTarget reports whether a target's data is out of date: its agent stopped
// sending heartbeats, or its last snapshot is older than three poll intervals
// (at least 15 minutes).
func StaleTarget(t store.Target, agentStale bool, now time.Time) bool {
	if t.LastSnapshotAt.IsZero() {
		return false
	}
	limit := 3 * time.Duration(t.PollIntervalSeconds) * time.Second
	if limit < 15*time.Minute {
		limit = 15 * time.Minute
	}
	return agentStale || now.Sub(t.LastSnapshotAt) > limit
}

// DriftsAt returns the open drifts of a service in an environment.
func (o Overview) DriftsAt(serviceID, envID string) []store.Drift {
	return o.Drifts[serviceID+"|"+envID]
}

// LoadOverview reads the matrix, upstream state and open drifts of a workspace.
func LoadOverview(ctx context.Context, st *store.Store, sc store.Scope) (Overview, error) {
	o := Overview{
		Upstreams: map[string]Upstream{}, Drifts: map[string][]store.Drift{}, Policies: map[string]Policy{},
		Services: map[string]store.Service{}, Envs: map[string]store.Environment{}, CheckErrs: map[string]string{},
		Stale: map[string]time.Time{},
	}
	services, err := st.ListServices(ctx, sc)
	if err != nil {
		return o, err
	}
	envs, err := st.ListEnvironments(ctx, sc)
	if err != nil {
		return o, err
	}
	if o.Targets, err = st.ListTargets(ctx, sc); err != nil {
		return o, err
	}
	agents, err := st.ListAgents(ctx, sc)
	if err != nil {
		return o, err
	}
	agentStale := map[string]bool{}
	for _, a := range agents {
		agentStale[a.ID] = !a.StaleSince.IsZero()
	}
	now := time.Now()
	for _, t := range o.Targets {
		if StaleTarget(t, agentStale[t.AgentID], now) {
			o.Stale[t.ID] = t.LastSnapshotAt
		}
	}
	active, err := st.ListActiveInstances(ctx, sc)
	if err != nil {
		return o, err
	}
	o.Matrix = BuildMatrix(services, envs, o.Targets, active)
	o.Refs = References(o.Matrix, active)
	for _, s := range services {
		o.Services[s.ID] = s
	}
	for _, e := range envs {
		o.Envs[e.ID] = e
	}
	for id, ref := range o.Refs {
		if _, msg, err := st.UpstreamStatus(ctx, sc, id); err == nil && msg != "" {
			o.CheckErrs[id] = msg
		}
		p, _ := ParsePolicy(o.Services[id].VersionPolicy)
		o.Policies[id] = p
		releases, err := st.ListReleases(ctx, sc, id)
		if err != nil {
			return o, err
		}
		if len(releases) == 0 {
			continue
		}
		tags := make([]string, len(releases))
		for i, r := range releases {
			tags[i] = r.Version
		}
		o.Upstreams[id] = Latest(tags, ref.Tag, p)
	}
	drifts, err := st.OpenDrifts(ctx, sc)
	if err != nil {
		return o, err
	}
	for _, d := range drifts {
		k := d.ServiceID + "|" + d.EnvironmentID
		o.Drifts[k] = append(o.Drifts[k], d)
	}
	return o, nil
}
