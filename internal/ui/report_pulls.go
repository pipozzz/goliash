// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package ui

import (
	"context"
	"slices"
	"time"

	"github.com/pipozzz/goliash/internal/store"
	"github.com/pipozzz/goliash/internal/versions"
)

// ReportPulls is what became of the pull requests Goliash opened during a month.
type ReportPulls struct {
	Opened, Merged, Closed, Open int
	Auto                         int    // opened by a version policy
	Rollbacks                    int    // to an older version
	ToRunning                    string // median from opening to the version running there; "" when none ran
	Ran                          int    // how many ran
}

// reportPulls counts the pull requests opened in [from, to).
func (s *Server) reportPulls(ctx context.Context, sc store.Scope, from, to time.Time) (ReportPulls, error) {
	var r ReportPulls
	pulls, err := s.store.ListGitOpsPulls(ctx, sc)
	if err != nil {
		return r, err
	}
	events := map[string][]store.Event{} // per service, version changes from the month on
	var ran []time.Duration
	for _, p := range pulls {
		if p.OpenedAt.Before(from) || !p.OpenedAt.Before(to) {
			continue
		}
		r.Opened++
		switch p.State {
		case "merged":
			r.Merged++
		case "closed":
			r.Closed++
		default:
			r.Open++
		}
		if p.OpenedBy == "auto-pr" {
			r.Auto++
		}
		if versions.IsRollback(p.From, p.To) {
			r.Rollbacks++
		}
		evs, ok := events[p.ServiceID]
		if !ok {
			evs, err = s.store.ListEvents(ctx, sc, store.EventFilter{
				ServiceID: p.ServiceID, Types: []string{"version_changed", "deployed"}, Since: from, Limit: 1000,
			})
			if err != nil {
				return r, err
			}
			events[p.ServiceID] = evs
		}
		first := time.Duration(-1)
		for _, e := range evs {
			if e.TargetID == p.TargetID && e.ToVersion == p.To && !e.At.Before(p.OpenedAt) {
				first = e.At.Sub(p.OpenedAt) // newest first: the last one kept is the first arrival
			}
		}
		if first >= 0 {
			ran = append(ran, first)
		}
	}
	if len(ran) > 0 {
		slices.Sort(ran)
		r.ToRunning, r.Ran = versions.HumanDuration(ran[len(ran)/2]), len(ran)
	}
	return r, nil
}
