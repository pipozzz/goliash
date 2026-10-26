// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package versions

import (
	"context"
	"sort"
	"time"

	"github.com/pipozzz/goliash/internal/store"
)

// How fast teams patch: each upgrade deployed to production closes an exposure, from when the first release after
// the old version came out to the deploy. Its days are the time to patch.

// PatchSample is one upgrade deployed to production and how long the service ran behind before it.
type PatchSample struct {
	ServiceID                string
	Service, Owner, From, To string
	At, BehindSince          time.Time
	Days                     int
}

// PatchTimes are the upgrades deployed to environment since since, oldest first: the version each replaced, the
// first release after it (its publication date, else when Goliash found it) and the deploy. Rollbacks and
// versions Goliash knows no release of are left out; a version deployed to several targets counts once, first.
func PatchTimes(ctx context.Context, st *store.Store, sc store.Scope, o Overview, envID string, since time.Time) ([]PatchSample, error) {
	if envID == "" {
		return nil, nil
	}
	evs, err := st.ListEvents(ctx, sc, store.EventFilter{EnvironmentID: envID, Types: []string{"version_changed"}, Since: since, Limit: 10000})
	if err != nil {
		return nil, err
	}
	releases, err := st.ReleasesByService(ctx, sc)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var out []PatchSample
	for i := len(evs) - 1; i >= 0; i-- { // newest first from the store: oldest first here
		e := evs[i]
		from, ok1 := ParseVersion(e.FromVersion)
		to, ok2 := ParseVersion(e.ToVersion)
		if e.ServiceID == "" || !ok1 || !ok2 || to.Compare(from) <= 0 || seen[e.ServiceID+"|"+e.ToVersion] {
			continue
		}
		var behind time.Time
		for _, r := range releases[e.ServiceID] {
			v, ok := ParseVersion(r.Version)
			if !ok || v.Compare(from) <= 0 || v.Compare(to) > 0 {
				continue
			}
			at := r.PublishedAt
			if at.IsZero() {
				at = r.DiscoveredAt
			}
			if !at.IsZero() && (behind.IsZero() || at.Before(behind)) {
				behind = at
			}
		}
		if behind.IsZero() || behind.After(e.At) {
			continue
		}
		seen[e.ServiceID+"|"+e.ToVersion] = true
		svc := o.Services[e.ServiceID]
		out = append(out, PatchSample{
			ServiceID: e.ServiceID, Service: svc.Name, Owner: svc.Owner, From: e.FromVersion, To: e.ToVersion, At: e.At, BehindSince: behind,
			Days: int(e.At.Sub(behind).Hours() / 24),
		})
	}
	return out, nil
}

// TeamPatching is how a team (the services' owner) patches production: what runs behind now, what is past its
// deadline, and how long its upgrades took in the last 90 days, month by month.
type TeamPatching struct {
	Owner        string // "" for services without one
	Services     int    // running in production
	Behind       int    // of them, behind a newer release
	MedianBehind int    // days, over those
	Overdue      int    // upgrades past their remediation deadline
	Patched      int    // upgrades deployed in the last 90 days
	MedianPatch  int    // days from the first newer release to the deploy, over those
	Monthly      []int  // median days to patch per month, oldest first (-1: no upgrade that month)
}

// PatchMonths is how many months the team trend and the time-to-patch chart cover.
const PatchMonths = 6

// LoadTeamPatching sums production up per team, slowest first: most past their deadline, then longest behind.
// It returns the upgrades it counted too, for PatchByMonth.
func LoadTeamPatching(ctx context.Context, st *store.Store, sc store.Scope, o Overview, pos Posture, updates []Update, now time.Time) ([]TeamPatching, []PatchSample, error) {
	samples, err := PatchTimes(ctx, st, sc, o, pos.Prod.ID, monthStart(now).AddDate(0, -(PatchMonths-1), 0))
	if err != nil {
		return nil, nil, err
	}
	teams := map[string]*TeamPatching{}
	team := func(owner string) *TeamPatching {
		t := teams[owner]
		if t == nil {
			t = &TeamPatching{Owner: owner}
			teams[owner] = t
		}
		return t
	}
	prodCol := -1
	for i, e := range o.Matrix.Environments {
		if e.ID == pos.Prod.ID {
			prodCol = i
		}
	}
	running := map[string]bool{}
	for _, row := range o.Matrix.Rows {
		if prodCol >= 0 && prodCol < len(row.Cells) && len(row.Cells[prodCol].Versions) > 0 && !running[row.Service.ID] {
			running[row.Service.ID] = true
			team(row.Service.Owner).Services++
		}
	}
	longest := map[string]Exposure{} // per service: its longest exposure, a service used by several apps counts once
	for _, ex := range pos.Exposures {
		if ex.Environment.ID != pos.Prod.ID || ex.Acked {
			continue
		}
		if l, ok := longest[ex.Service.ID]; !ok || ex.Days > l.Days {
			longest[ex.Service.ID] = ex
		}
	}
	behind := map[string][]int{}
	for _, ex := range longest {
		t := team(ex.Service.Owner)
		t.Behind++
		behind[t.Owner] = append(behind[t.Owner], ex.Days)
	}
	for _, u := range updates {
		if !u.Acked && u.Overdue(now) && u.Environment.ID == pos.Prod.ID {
			team(u.Service.Owner).Overdue++
		}
	}
	recent := map[string][]int{}
	monthly := map[string][][]int{}
	first := monthStart(now).AddDate(0, -(PatchMonths - 1), 0)
	for _, s := range samples {
		if now.Sub(s.At) <= 90*24*time.Hour {
			recent[s.Owner] = append(recent[s.Owner], s.Days)
		}
		m := monthsBetween(first, s.At)
		if m < 0 || m >= PatchMonths {
			continue
		}
		if monthly[s.Owner] == nil {
			monthly[s.Owner] = make([][]int, PatchMonths)
		}
		monthly[s.Owner][m] = append(monthly[s.Owner][m], s.Days)
		team(s.Owner)
	}
	out := make([]TeamPatching, 0, len(teams))
	for owner, t := range teams {
		t.MedianBehind = medianInt(behind[owner])
		t.Patched, t.MedianPatch = len(recent[owner]), medianInt(recent[owner])
		t.Monthly = make([]int, PatchMonths)
		for i := range t.Monthly {
			t.Monthly[i] = -1
			if months := monthly[owner]; months != nil && len(months[i]) > 0 {
				t.Monthly[i] = medianInt(months[i])
			}
		}
		out = append(out, *t)
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Overdue != b.Overdue {
			return a.Overdue > b.Overdue
		}
		if a.MedianBehind != b.MedianBehind {
			return a.MedianBehind > b.MedianBehind
		}
		return a.Owner < b.Owner
	})
	return out, samples, nil
}

// PatchMonth is production's time to patch in one month: the median and 90th percentile days, over its upgrades.
type PatchMonth struct {
	Month       time.Time
	Upgrades    int
	Median, P90 int
}

// PatchByMonth is production's time to patch month by month, the last PatchMonths, oldest first.
func PatchByMonth(samples []PatchSample, now time.Time) []PatchMonth {
	first := monthStart(now).AddDate(0, -(PatchMonths - 1), 0)
	days := make([][]int, PatchMonths)
	for _, s := range samples {
		if m := monthsBetween(first, s.At); m >= 0 && m < PatchMonths {
			days[m] = append(days[m], s.Days)
		}
	}
	out := make([]PatchMonth, PatchMonths)
	for i := range out {
		out[i] = PatchMonth{Month: first.AddDate(0, i, 0), Upgrades: len(days[i]), Median: medianInt(days[i]), P90: percentile(days[i], 90)}
	}
	return out
}

func monthStart(t time.Time) time.Time {
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC)
}

// monthsBetween is how many months t is after the month first starts.
func monthsBetween(first, t time.Time) int {
	t = t.UTC()
	return (t.Year()-first.Year())*12 + int(t.Month()) - int(first.Month())
}

func medianInt(xs []int) int { return percentile(xs, 50) }
