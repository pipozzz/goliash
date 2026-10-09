// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package versions

import (
	"context"
	"sort"
	"time"

	"github.com/pipozzz/goliash/internal/store"
)

// Exposure is one service in one environment running a version with a newer release out: how long
// it has been exposed, counted from when the first newer release was published.
type Exposure struct {
	Update
	BehindSince time.Time // when the first release newer than Running came out (or was first seen)
	Days        int
	Known       bool // BehindSince is a publication date; otherwise it is when Goliash first saw the release, a lower bound
}

// Posture is the security view of a workspace: patch latency, end of life and blind spots.
type Posture struct {
	Prod        store.Environment // the last environment, where exposure counts most
	Exposures   []Exposure        // every environment, longest exposed first
	Prod30      int               // production exposures older than 30 days
	Prod90      int
	ProdMedian  int // median days of production exposure, over those whose release dates are known
	ProdP90     int
	EOLPassed   int // production services on a release cycle past its end of life
	EOLSoon     int
	Accepted    int // production exposures quieted by an acknowledgement: accepted risk
	Stale       []string
	NoUpstream  []string // services whose newest release is unknown, so exposure cannot be measured
	ProdRunning int      // services running in production
}

// LoadPosture computes the posture from the overview, the releases and the acknowledgements.
func LoadPosture(ctx context.Context, st *store.Store, sc store.Scope, o Overview, now time.Time) (Posture, error) {
	var p Posture
	for _, e := range o.Matrix.Environments {
		if p.Prod.ID == "" || e.Position >= p.Prod.Position {
			p.Prod = e
		}
	}
	acks, err := st.ListAcks(ctx, sc)
	if err != nil {
		return p, err
	}
	released := map[string][]store.Release{}
	for _, u := range Updates(o, acks, now) {
		if u.Target == "" && u.EOL == "" {
			continue
		}
		rels, ok := released[u.Service.ID]
		if !ok {
			if rels, err = st.ListReleases(ctx, sc, u.Service.ID); err != nil {
				return p, err
			}
			released[u.Service.ID] = rels
		}
		ex := Exposure{Update: u}
		ex.BehindSince, ex.Known = behindSince(u.Running, rels, u.Since)
		ex.Days = int(now.Sub(ex.BehindSince).Hours() / 24)
		p.Exposures = append(p.Exposures, ex)
	}
	sort.SliceStable(p.Exposures, func(i, j int) bool {
		a, b := p.Exposures[i], p.Exposures[j]
		if (a.Environment.ID == p.Prod.ID) != (b.Environment.ID == p.Prod.ID) {
			return a.Environment.ID == p.Prod.ID
		}
		return a.Days > b.Days
	})
	var days []int
	for _, ex := range p.Exposures {
		if ex.Environment.ID != p.Prod.ID {
			continue
		}
		if ex.Acked {
			p.Accepted++
		}
		if ex.EOL != "" {
			if ex.EOLPassed {
				p.EOLPassed++
			} else {
				p.EOLSoon++
			}
		}
		if ex.Target == "" {
			continue
		}
		if ex.Known {
			days = append(days, ex.Days)
		}
		if ex.Days > 30 {
			p.Prod30++
		}
		if ex.Days > 90 {
			p.Prod90++
		}
	}
	p.ProdMedian, p.ProdP90 = percentile(days, 50), percentile(days, 90)
	for _, t := range o.Targets {
		if _, ok := o.Stale[t.ID]; ok {
			p.Stale = append(p.Stale, t.Name)
		}
	}
	sort.Strings(p.Stale)
	for _, row := range o.Matrix.Rows {
		ei := -1
		for i, e := range o.Matrix.Environments {
			if e.ID == p.Prod.ID {
				ei = i
			}
		}
		if ei < 0 || row.Cells[ei].Empty() {
			continue
		}
		p.ProdRunning++
		if up, ok := o.Upstreams[row.Service.ID]; !ok || !up.HasLatest {
			p.NoUpstream = append(p.NoUpstream, row.Service.Name)
		}
	}
	sort.Strings(p.NoUpstream)
	return p, nil
}

// behindSince is when the first release newer than running came out: its publication date when known,
// else when Goliash first saw it (known is then false); fallback when no newer release is known.
func behindSince(running string, rels []store.Release, fallback time.Time) (at time.Time, known bool) {
	cur, ok := ParseVersion(running)
	if !ok {
		return fallback, false
	}
	var first time.Time
	firstKnown := false
	for _, r := range rels {
		v, ok := ParseVersion(r.Version)
		if !ok || v.Compare(cur) <= 0 || v.Pre != "" {
			continue
		}
		at, pub := r.PublishedAt, true
		if at.IsZero() {
			at, pub = r.DiscoveredAt, false
		}
		if !at.IsZero() && (first.IsZero() || at.Before(first)) {
			first, firstKnown = at, pub
		}
	}
	if first.IsZero() {
		return fallback, false
	}
	return first, firstKnown
}

// percentile is the p-th percentile (nearest rank) of values, 0 when there are none.
func percentile(values []int, p int) int {
	if len(values) == 0 {
		return 0
	}
	s := append([]int(nil), values...)
	sort.Ints(s)
	i := (p*len(s) + 99) / 100
	return s[max(i-1, 0)]
}
