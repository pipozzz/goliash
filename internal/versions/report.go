// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package versions

import (
	"context"
	"encoding/json"
	"sort"
	"time"

	"github.com/pipozzz/goliash/internal/store"
)

// Report is a month in one workspace, for a monthly review or an MSP's client report:
// what was delivered during the month, and what needs attention now.
type Report struct {
	From, To  time.Time // the month, [From, To)
	Generated time.Time
	Services  int
	Targets   int
	Envs      []store.Environment
	Delivery  []Delivery // deploys and lead times during the month
	Deploys   int        // versions that arrived anywhere during the month
	Releases  []ReportRelease
	Attention []ReportItem // open drift now, most important first
}

// ReportRelease is an upstream release first seen during the month.
type ReportRelease struct {
	Service, Version string
	At               time.Time
}

// ReportItem is one open drift.
type ReportItem struct {
	Service, Environment, Kind string
	Detail                     DriftDetail
	Since                      time.Time
}

// kindOrder sorts what needs attention: end of life and Git drift first, then lagging.
var kindOrder = map[string]int{"eol": 0, "declared": 1, "inconsistent": 2, "upstream": 3, "env": 4}

// MonthReport builds the report for the month that starts at from (UTC).
func MonthReport(ctx context.Context, st *store.Store, sc store.Scope, from, now time.Time) (Report, error) {
	from = time.Date(from.Year(), from.Month(), 1, 0, 0, 0, 0, time.UTC)
	to := from.AddDate(0, 1, 0)
	end := to
	if now.Before(end) {
		end = now // the current month so far
	}
	o, err := LoadOverview(ctx, st, sc)
	if err != nil {
		return Report{}, err
	}
	r := Report{From: from, To: to, Generated: now, Services: len(o.Matrix.Rows), Targets: len(o.Targets), Envs: o.Matrix.Environments}
	if r.Delivery, err = DeliveryStats(ctx, st, sc, o, end.Sub(from), end); err != nil {
		return Report{}, err
	}
	for _, d := range r.Delivery {
		for _, e := range d.Envs {
			r.Deploys += e.Deploys
		}
	}
	evs, err := st.ListEvents(ctx, sc, store.EventFilter{Types: []string{"new_release"}, Since: from, Before: end, Limit: 1000})
	if err != nil {
		return Report{}, err
	}
	for _, e := range evs {
		r.Releases = append(r.Releases, ReportRelease{Service: o.Services[e.ServiceID].Name, Version: e.ToVersion, At: e.At})
	}
	for _, ds := range o.Drifts {
		for _, d := range ds {
			var det DriftDetail
			_ = json.Unmarshal(d.Detail, &det)
			r.Attention = append(r.Attention, ReportItem{
				Service: o.Services[d.ServiceID].Name, Environment: o.Envs[d.EnvironmentID].Name,
				Kind: d.Kind, Detail: det, Since: d.Since,
			})
		}
	}
	envPos := map[string]int{}
	for _, e := range o.Matrix.Environments {
		envPos[e.Name] = e.Position
	}
	sort.Slice(r.Attention, func(i, j int) bool {
		a, b := r.Attention[i], r.Attention[j]
		if kindOrder[a.Kind] != kindOrder[b.Kind] {
			return kindOrder[a.Kind] < kindOrder[b.Kind]
		}
		if envPos[a.Environment] != envPos[b.Environment] {
			return envPos[a.Environment] > envPos[b.Environment] // prod first
		}
		return a.Service < b.Service
	})
	return r, nil
}
