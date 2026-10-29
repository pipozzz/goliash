// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package versions

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/pipozzz/goliash/internal/registry"
	"github.com/pipozzz/goliash/internal/store"
)

// Overview is everything the matrix view shows for a workspace.
type Overview struct {
	Matrix    Matrix
	Upstreams map[string]Upstream          // service ID -> newest acceptable release
	Refs      map[string]Reference         // service ID -> upstream repo and reference tag
	Drifts    map[string][]store.Drift     // service ID + "|" + environment ID -> open drifts, each once
	AppDrifts map[string][]store.Drift     // service ID + "|" + app + "|" + environment ID, for services split by application
	Policies  map[string]Policy            // service ID -> policy
	Services  map[string]store.Service     // by ID
	Envs      map[string]store.Environment // by ID
	Targets   []store.Target
	CheckErrs map[string]string // service ID -> last upstream check error
	// CheckedAt is when each service's upstream was last looked at; TagCount how many of its tags Goliash knows.
	CheckedAt map[string]time.Time
	TagCount  map[string]int
	// Stale lists targets whose data may be out of date (agent silent, or no recent
	// snapshot), with their last snapshot time.
	Stale map[string]time.Time
	// ReleaseURL maps service ID + "|" + version to its release notes link.
	ReleaseURL map[string]string
	// ReleaseSecurity maps service ID + "|" + version to the security fixes that release names.
	ReleaseSecurity map[string]string
	// ReleaseSecurityAt maps service ID + "|" + version to when Goliash found that release fixing vulnerabilities.
	ReleaseSecurityAt map[string]time.Time
	// ReleaseBreaking maps service ID + "|" + version to what that release's notes say breaks.
	ReleaseBreaking map[string]string
	// SplitApps lists services whose versions are compared per application.
	SplitApps map[string]bool
	// Resolved maps image digests to the exact release behind a moving tag.
	Resolved map[string]string
	// Registries holds the hosts the workspace gave the server credentials for, secrets left out.
	Registries RegistryAuth
}

// StaleTarget reports whether a target's data is out of date: its agent stopped
// sending heartbeats, or its last snapshot is older than three poll intervals
// (at least 15 minutes).
func StaleTarget(t store.Target, agentStale bool, now time.Time) bool {
	if t.LastSnapshotAt.IsZero() || Manual(t.Platform) {
		return false
	}
	limit := 3 * time.Duration(t.PollIntervalSeconds) * time.Second
	if limit < 15*time.Minute {
		limit = 15 * time.Minute
	}
	return agentStale || now.Sub(t.LastSnapshotAt) > limit
}

// DriftsAt returns the open drifts of a service in an environment, in every
// application when it splits by application.
func (o Overview) DriftsAt(serviceID, envID string) []store.Drift {
	return o.Drifts[serviceID+"|"+envID]
}

// DriftsIn returns the open drifts of a service within one application in an
// environment. A service that does not split by application drifts as a whole, so
// any app gets all its drifts.
func (o Overview) DriftsIn(serviceID, app, envID string) []store.Drift {
	if o.SplitApps[serviceID] {
		return o.AppDrifts[serviceID+"|"+app+"|"+envID]
	}
	return o.Drifts[serviceID+"|"+envID]
}

// LoadOverview reads the matrix, upstream state and open drifts of a workspace.
func LoadOverview(ctx context.Context, st *store.Store, sc store.Scope) (Overview, error) {
	return memoized(ctx, "overview "+sc.WorkspaceID, func() (Overview, error) { return loadOverview(ctx, st, sc) })
}

func loadOverview(ctx context.Context, st *store.Store, sc store.Scope) (Overview, error) {
	o := Overview{
		Upstreams: map[string]Upstream{}, Drifts: map[string][]store.Drift{}, AppDrifts: map[string][]store.Drift{}, Policies: map[string]Policy{},
		Services: map[string]store.Service{}, Envs: map[string]store.Environment{}, CheckErrs: map[string]string{},
		Stale: map[string]time.Time{}, ReleaseURL: map[string]string{}, CheckedAt: map[string]time.Time{}, TagCount: map[string]int{},
		ReleaseSecurity:   map[string]string{},
		ReleaseBreaking:   map[string]string{},
		ReleaseSecurityAt: map[string]time.Time{},
	}
	services, err := st.ListServices(ctx, sc)
	if err != nil {
		return o, err
	}
	creds, err := st.ListRegistryCredentials(ctx, sc)
	if err != nil {
		return o, err
	}
	o.Registries = RegistryAuth{}
	for _, c := range creds {
		o.Registries[c.Host] = registry.Credentials{}
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
	if active, err = namedInstances(ctx, st, sc, active, services); err != nil {
		return o, err
	}
	o.Matrix = BuildMatrix(services, envs, o.Targets, active)
	if o.Resolved, err = resolvedDigests(ctx, st, sc); err != nil {
		return o, err
	}
	applyResolutions(&o.Matrix, o.Resolved)
	o.SplitApps = splitApps(o.Matrix)
	o.Refs = References(o.Matrix, active)
	for _, s := range services {
		o.Services[s.ID] = s
	}
	for _, e := range envs {
		o.Envs[e.ID] = e
	}
	checks, err := st.UpstreamChecks(ctx, sc)
	if err != nil {
		return o, err
	}
	for id := range o.Refs {
		if msg := checks[id].Error; msg != "" {
			o.CheckErrs[id] = msg
		}
		o.CheckedAt[id] = checks[id].At
	}
	if err := loadUpstreams(ctx, st, sc, &o, nil); err != nil {
		return o, err
	}
	drifts, err := st.OpenDrifts(ctx, sc)
	if err != nil {
		return o, err
	}
	for _, d := range drifts {
		k := d.ServiceID + "|" + d.EnvironmentID
		o.Drifts[k] = append(o.Drifts[k], d)
		if d.App != "" {
			k = d.ServiceID + "|" + d.App + "|" + d.EnvironmentID
			o.AppDrifts[k] = append(o.AppDrifts[k], d)
		}
	}
	return o, nil
}

func splitApps(m Matrix) map[string]bool {
	out := map[string]bool{}
	for _, r := range m.Rows {
		if len(r.Parts) > 0 {
			out[r.Service.ID] = true
		}
	}
	return out
}

// loadUpstreams fills each referenced service's policy and newest acceptable release. With at, only releases
// out by then count: published by then when the date is known, else seen by Goliash by then.
func loadUpstreams(ctx context.Context, st *store.Store, sc store.Scope, o *Overview, at *time.Time) error {
	all, err := releasesByService(ctx, st, sc)
	if err != nil {
		return err
	}
	for id, ref := range o.Refs {
		p, _, _ := PolicyFor(o.Services[id], ref.Repo)
		o.Policies[id] = p
		releases := all[id]
		var tags []string
		for _, r := range releases {
			if at != nil {
				out := r.DiscoveredAt
				if !r.PublishedAt.IsZero() {
					out = r.PublishedAt
				}
				if out.After(*at) {
					continue
				}
			}
			tags = append(tags, r.Version)
			if r.ChangelogURL != "" {
				o.ReleaseURL[id+"|"+r.Version] = r.ChangelogURL
			}
			if r.Security != "" && o.ReleaseSecurity != nil {
				o.ReleaseSecurity[id+"|"+r.Version] = r.Security
				if o.ReleaseSecurityAt != nil {
					o.ReleaseSecurityAt[id+"|"+r.Version] = r.DiscoveredAt
				}
			}
			if r.Breaking != "" && o.ReleaseBreaking != nil {
				o.ReleaseBreaking[id+"|"+r.Version] = r.Breaking
			}
		}
		if o.TagCount != nil {
			o.TagCount[id] = len(tags)
		}
		if len(tags) > 0 {
			o.Upstreams[id] = Latest(tags, ref.Tag, p)
		}
	}
	return nil
}

// releaseCacheTTL bounds how long a workspace's releases are reused; writes in this process drop them sooner.
const releaseCacheTTL = time.Minute

var releaseCache struct {
	sync.Mutex
	m map[string]cachedReleases
}

type cachedReleases struct {
	gen  uint64
	at   time.Time
	data map[string][]store.Release
}

// releasesByService is store.ReleasesByService, reused across page loads until releases change: the largest
// read of a matrix, and the same for a minute at a time.
func releasesByService(ctx context.Context, st *store.Store, sc store.Scope) (map[string][]store.Release, error) {
	gen := st.ReleasesGeneration()
	key := fmt.Sprintf("%p|%s", st, sc.WorkspaceID)
	releaseCache.Lock()
	c, ok := releaseCache.m[key]
	releaseCache.Unlock()
	if ok && c.gen == gen && time.Since(c.at) < releaseCacheTTL {
		return c.data, nil
	}
	data, err := st.ReleasesByService(ctx, sc)
	if err != nil {
		return nil, err
	}
	releaseCache.Lock()
	if releaseCache.m == nil {
		releaseCache.m = map[string]cachedReleases{}
	}
	releaseCache.m[key] = cachedReleases{gen: gen, at: time.Now(), data: data}
	releaseCache.Unlock()
	return data, nil
}
