// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package versions

import (
	"context"
	"slices"
	"time"

	"github.com/pipozzz/goliash/internal/store"
)

// PullTiming is a pull request Goliash opened, with what became of the version it pins where it was meant to run.
type PullTiming struct {
	store.GitOpsPull
	App            GitOpsApp     // the application it was opened for, when it still runs
	Known          bool          // App is set
	Deployed       bool          // the version it pins runs there now
	ToRunning      time.Duration // from opening it to the version running there; 0 unknown
	MergeToRunning time.Duration // from merging it to the version running there; 0 unknown
}

// Undeployed tells whether the pull request was merged and its version does not run where it was meant to.
func (p PullTiming) Undeployed() bool {
	return p.State == "merged" && p.Known && !p.Deployed && !p.MergedAt.IsZero()
}

// PullTimings are the pull requests Goliash opened since since, newest first, each with whether its version runs
// and how long it took to: from opening, and from merging (the forge's merge time).
func PullTimings(ctx context.Context, st *store.Store, sc store.Scope, apps []GitOpsApp, since time.Time) ([]PullTiming, error) {
	pulls, err := st.ListGitOpsPulls(ctx, sc)
	if err != nil {
		return nil, err
	}
	running := map[string]GitOpsApp{} // target|workload
	for _, a := range apps {
		running[a.TargetID+"|"+a.WorkloadID] = a
	}
	var out []PullTiming
	arrived := map[string][]store.Event{} // service -> its version changes since the oldest pull request
	for _, p := range pulls {
		if p.OpenedAt.Before(since) {
			continue
		}
		t := PullTiming{GitOpsPull: p}
		t.App, t.Known = running[p.TargetID+"|"+p.WorkloadID]
		t.Deployed = t.Known && t.App.Tag == p.To
		if t.Deployed {
			evs, ok := arrived[p.ServiceID]
			if !ok {
				evs, _ = st.ListEvents(ctx, sc, store.EventFilter{
					ServiceID: p.ServiceID, Types: []string{"version_changed", "deployed"}, Since: since.Add(-24 * time.Hour), Limit: 1000,
				})
				arrived[p.ServiceID] = evs
			}
			for _, e := range evs { // newest first: keep the first arrival after the pull request was opened
				if e.TargetID == p.TargetID && e.ToVersion == p.To && !e.At.Before(p.OpenedAt) {
					t.ToRunning = e.At.Sub(p.OpenedAt)
					if !p.MergedAt.IsZero() && !e.At.Before(p.MergedAt) {
						t.MergeToRunning = max(e.At.Sub(p.MergedAt), time.Second) // a sync within the second still counts
					} else {
						t.MergeToRunning = 0
					}
				}
			}
		}
		out = append(out, t)
	}
	return out, nil
}

// MedianDuration is the median of the durations above zero, and how many there are; 0 when none.
func MedianDuration(ds []time.Duration) (time.Duration, int) {
	var pos []time.Duration
	for _, d := range ds {
		if d > 0 {
			pos = append(pos, d)
		}
	}
	if len(pos) == 0 {
		return 0, 0
	}
	slices.Sort(pos)
	return pos[len(pos)/2], len(pos)
}
