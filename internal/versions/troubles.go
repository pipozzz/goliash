// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package versions

import (
	"context"
	"time"

	"github.com/pipozzz/goliash/internal/store"
)

// troubleWindow is how far back a rollback marks a release as troubled.
const troubleWindow = 90 * 24 * time.Hour

// Troubles are releases that gave trouble where they ran, by upstream repository and version ("docker.io/library/
// nginx|1.28.0"): rolled back within troubleWindow (a rollout flapping back and forth aside), or stuck rolling out
// or down now. Each says what happened and where: "rolled back in prod (payments) on 2026-11-09". Other services and
// teams running the same image learn from it before they upgrade.
func Troubles(ctx context.Context, st *store.Store, sc store.Scope, o Overview, now time.Time) (map[string]string, error) {
	out := map[string]string{}
	repoOf := func(svcID string) string { return o.Refs[svcID].Repo }
	envName := func(id string) string { return o.Envs[id].Name }
	evs, err := st.ListEvents(ctx, sc, store.EventFilter{Types: []string{"version_changed"}, Since: now.Add(-troubleWindow), Limit: maxDeliveryEvents})
	if err != nil {
		return nil, err
	}
	for i, e := range evs {
		if EventNote(e) != RollbackNote || flapBack(evs, i) {
			continue
		}
		repo := repoOf(e.ServiceID)
		if repo == "" || e.FromVersion == "" {
			continue
		}
		k := repo + "|" + e.FromVersion
		if _, seen := out[k]; !seen { // the newest first
			out[k] = "rolled back in " + envName(e.EnvironmentID) + " (" + o.Services[e.ServiceID].Name + ") on " + e.At.UTC().Format(time.DateOnly)
		}
	}
	issues, err := RolloutIssues(ctx, st, sc, o, now)
	if err != nil {
		return out, err
	}
	for _, is := range issues {
		if (is.Kind != RolloutStuck && is.Kind != RolloutDown) || len(is.Versions) == 0 {
			continue
		}
		repo := repoOf(is.ServiceID)
		if repo == "" {
			continue
		}
		k := repo + "|" + is.Versions[0].Version
		if _, seen := out[k]; !seen {
			what := "stuck rolling out"
			if is.Kind == RolloutDown {
				what = "down"
			}
			out[k] = what + " in " + is.Environment + " (" + is.Service + ")"
		}
	}
	return out, nil
}

// WithTroubles marks upgrades to a release, or through one, that gave trouble elsewhere: high risk, and why.
func WithTroubles(us []Update, o Overview, troubles map[string]string) []Update {
	if len(troubles) == 0 {
		return us
	}
	for i := range us {
		u := &us[i]
		if u.Target == "" {
			continue
		}
		repo := o.Refs[u.Service.ID].Repo
		stops := u.Path
		if len(stops) == 0 {
			stops = []string{u.Target}
		}
		for _, v := range stops {
			if t, ok := troubles[repo+"|"+v]; ok {
				u.Trouble = v + " " + t
				u.Risk = RiskHigh
				u.RiskReasons = append(u.RiskReasons, u.Trouble)
				break
			}
		}
	}
	return us
}
