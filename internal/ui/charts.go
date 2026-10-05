// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package ui

import (
	"fmt"
	"hash/fnv"
	"net/http"
	"sort"
	"time"

	"github.com/pipozzz/goliash/internal/auth"
	"github.com/pipozzz/goliash/internal/store"
)

// Charts are drawn on the server as SVG: no script (the Content-Security-Policy
// allows none inline), colours from CSS variables (dark mode), native tooltips.

// Series is one coloured line or stack of a chart.
type Series struct {
	Name   string
	Class  string // c0…c5: colour
	Values []int
}

// BarChart is a stacked bar chart over periods.
type BarChart struct {
	Labels []string // one per bar
	Series []Series
	Max    int // the y axis top, a round number
}

// LineChart is lines over days.
type LineChart struct {
	Labels []string // one per point
	Series []Series
	Max    int
}

// Timeline shows which version ran when, one lane per environment.
type Timeline struct {
	From, To time.Time
	Lanes    []Lane
	Ticks    []Tick
}

// Lane is one environment of a Timeline.
type Lane struct {
	Env      string
	Segments []Segment
}

// Segment is one version running between two moments, as fractions of the timeline.
type Segment struct {
	Version    string
	Start, End float64 // 0…1
	Class      string
	Title      string
}

// Tick is a date on a timeline's axis.
type Tick struct {
	At    float64
	Label string
}

func colour(i int) string { return fmt.Sprintf("c%d", i%6) }

// versionColour gives each version a stable colour.
func versionColour(v string) string {
	h := fnv.New32a()
	_, _ = h.Write([]byte(v))
	return colour(int(h.Sum32() % 6))
}

// niceMax is the top of an axis with four gridlines on round steps (1, 2 or 5 times a
// power of ten) that reaches n: 4, 8, 20, 40, 80, 200, …
func niceMax(n int) int {
	for p := 1; ; p *= 10 {
		for _, m := range []int{1, 2, 5} {
			if 4*m*p >= n {
				return 4 * m * p
			}
		}
	}
}

func startOfWeek(t time.Time) time.Time {
	t = t.UTC().Truncate(24 * time.Hour)
	wd := (int(t.Weekday()) + 6) % 7 // Monday = 0
	return t.AddDate(0, 0, -wd)
}

// deploysPerWeek counts versions arriving per environment per week, oldest first.
func deploysPerWeek(events []store.Event, envs []store.Environment, now time.Time, weeks int) BarChart {
	first := startOfWeek(now).AddDate(0, 0, -7*(weeks-1))
	c := BarChart{}
	for i := range weeks {
		c.Labels = append(c.Labels, first.AddDate(0, 0, 7*i).Format("Jan 2"))
	}
	index := map[string]int{}
	for i, e := range envs {
		index[e.ID] = i
		c.Series = append(c.Series, Series{Name: e.Name, Class: colour(i), Values: make([]int, weeks)})
	}
	for _, ev := range events {
		if ev.Type != "deployed" && ev.Type != "version_changed" {
			continue
		}
		i, ok := index[ev.EnvironmentID]
		w := int(ev.At.UTC().Sub(first).Hours() / (24 * 7))
		if !ok || ev.At.Before(first) || w >= weeks {
			continue
		}
		c.Series[i].Values[w]++
	}
	top := 0
	for w := range weeks {
		sum := 0
		for _, s := range c.Series {
			sum += s.Values[w]
		}
		top = max(top, sum)
	}
	c.Max = niceMax(top)
	return c
}

var driftKinds = []struct{ kind, label string }{
	{"env", "behind previous env"},
	{"upstream", "behind upstream"},
	{"eol", "end of life"},
	{"inconsistent", "targets disagree"},
	{"declared", "differs from Git"},
}

// openDriftPerDay counts drifts open at the end of each day, per kind, oldest first.
func openDriftPerDay(drifts []store.Drift, now time.Time, days int) LineChart {
	end := now.UTC().Truncate(24*time.Hour).AddDate(0, 0, 1)
	c := LineChart{}
	for d := range days {
		c.Labels = append(c.Labels, end.AddDate(0, 0, d-days).Format("Jan 2"))
	}
	top := 0
	for k, dk := range driftKinds {
		s := Series{Name: dk.label, Class: colour(k), Values: make([]int, days)}
		for d := range days {
			at := end.AddDate(0, 0, d-days+1)
			if at.After(now) {
				at = now
			}
			for _, dr := range drifts {
				if dr.Kind == dk.kind && !dr.Since.After(at) && (dr.ResolvedAt.IsZero() || dr.ResolvedAt.After(at)) {
					s.Values[d]++
				}
			}
			top = max(top, s.Values[d])
		}
		c.Series = append(c.Series, s)
	}
	c.Max = niceMax(top)
	return c
}

// versionTimeline turns a service's deploy events into lanes of versions per
// environment over the window ending now, one lane per target where an environment
// has several. events may come in any order.
func versionTimeline(events []store.Event, envs []store.Environment, targets map[string]string, now time.Time, window time.Duration) Timeline {
	from := now.Add(-window)
	t := Timeline{From: from, To: now}
	type key struct{ env, target string }
	byLane := map[key][]store.Event{}
	targetsOf := map[string]map[string]bool{}
	for _, ev := range events {
		switch ev.Type {
		case "deployed", "version_changed", "removed":
			byLane[key{ev.EnvironmentID, ev.TargetID}] = append(byLane[key{ev.EnvironmentID, ev.TargetID}], ev)
			if targetsOf[ev.EnvironmentID] == nil {
				targetsOf[ev.EnvironmentID] = map[string]bool{}
			}
			targetsOf[ev.EnvironmentID][ev.TargetID] = true
		}
	}
	for _, env := range envs {
		ids := make([]string, 0, len(targetsOf[env.ID]))
		for id := range targetsOf[env.ID] {
			ids = append(ids, id)
		}
		sort.Slice(ids, func(i, j int) bool { return targets[ids[i]] < targets[ids[j]] })
		for _, tid := range ids {
			name := env.Name
			if len(ids) > 1 && targets[tid] != "" {
				name += " · " + targets[tid]
			}
			if lane, ok := buildLane(byLane[key{env.ID, tid}], name, from, now, window); ok {
				t.Lanes = append(t.Lanes, lane)
			}
		}
	}
	for i := 0; i <= 4; i++ {
		at := from.Add(window * time.Duration(i) / 4)
		t.Ticks = append(t.Ticks, Tick{At: float64(i) / 4, Label: at.UTC().Format("Jan 2")})
	}
	return t
}

func buildLane(evs []store.Event, name string, from, now time.Time, window time.Duration) (Lane, bool) {
	frac := func(at time.Time) float64 {
		if at.Before(from) {
			return 0
		}
		return float64(at.Sub(from)) / float64(window)
	}
	sort.Slice(evs, func(i, j int) bool { return evs[i].At.Before(evs[j].At) })
	lane := Lane{Env: name}
	for i, ev := range evs {
		if ev.Type == "removed" {
			continue
		}
		endAt := now
		if i+1 < len(evs) {
			endAt = evs[i+1].At
		}
		if !endAt.After(from) {
			continue
		}
		seg := Segment{Version: ev.ToVersion, Start: frac(ev.At), End: frac(endAt), Class: versionColour(ev.ToVersion)}
		seg.Title = fmt.Sprintf("%s in %s from %s", ev.ToVersion, name, ev.At.UTC().Format("Jan 2 15:04"))
		if i+1 < len(evs) {
			seg.Title += " to " + endAt.UTC().Format("Jan 2 15:04")
		} else {
			seg.Title += ", still running"
		}
		lane.Segments = append(lane.Segments, seg)
	}
	return lane, len(lane.Segments) > 0
}

// f formats an SVG coordinate.
func f(v float64) string { return fmt.Sprintf("%.1f", v) }

// yAt is the y coordinate of value v on an axis from 0 to top.
func yAt(v, top int) float64 {
	if top == 0 {
		return chartH
	}
	return chartH - float64(v)/float64(top)*(chartH-8)
}

// barX is the left edge and width of bar i of n.
func barX(i, n int) (float64, float64) {
	slot := (chartW - chartLeft) / float64(n)
	return chartLeft + float64(i)*slot + slot*0.18, slot * 0.64
}

func pointX(i, n int) float64 {
	if n < 2 {
		return chartLeft
	}
	return chartLeft + 8 + float64(i)/float64(n-1)*(chartW-chartLeft-16)
}

func polyline(values []int, top int) string {
	out := ""
	for i, v := range values {
		if i > 0 {
			out += " "
		}
		out += f(pointX(i, len(values))) + "," + f(yAt(v, top))
	}
	return out
}

func lastPoint(values []int, top int) (float64, float64) {
	i := len(values) - 1
	return pointX(i, len(values)), yAt(values[i], top)
}

func anyNonZero(values []int) bool {
	for _, v := range values {
		if v != 0 {
			return true
		}
	}
	return false
}

func nonZeroSeries(series []Series) []Series {
	var out []Series
	for _, s := range series {
		if anyNonZero(s.Values) {
			out = append(out, s)
		}
	}
	return out
}

func tickAnchor(i, n int) string {
	switch i {
	case 0:
		return "start"
	case n - 1:
		return "end"
	}
	return "middle"
}

// deliveryCharts adds deploys per week (12 weeks) and open drift per day (30 days).
func (s *Server) deliveryCharts(r *http.Request, p auth.Principal, v *PromotionsView) error {
	ctx, now := r.Context(), time.Now()
	envs, err := s.store.ListEnvironments(ctx, p.Scope)
	if err != nil {
		return err
	}
	evs, err := s.store.ListEvents(ctx, p.Scope, store.EventFilter{
		Types: []string{"deployed", "version_changed"}, Since: startOfWeek(now).AddDate(0, 0, -7*11), Limit: 20000,
	})
	if err != nil {
		return err
	}
	v.Deploys = deploysPerWeek(evs, envs, now, 12)
	drifts, err := s.store.DriftsBetween(ctx, p.Scope, now.AddDate(0, 0, -30), now)
	if err != nil {
		return err
	}
	v.Drift = openDriftPerDay(drifts, now, 30)
	return nil
}
