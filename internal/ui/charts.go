// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package ui

import (
	"encoding/json"
	"fmt"
	"hash/fnv"
	"math"
	"net/http"
	"sort"
	"strings"
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

// driftKinds are the lines of the drift chart, coloured like their badges.
var driftKinds = []struct{ kind, label, class string }{
	{"env", "behind previous env", "c3"},
	{"upstream", "behind upstream", "c1"},
	{"eol", "end of life", "c4"},
	{"inconsistent", "targets disagree", "c5"},
	{"declared", "differs from Git", "c2"},
}

// openDriftPerDay counts drifts open at the end of each day, per kind, oldest first.
func openDriftPerDay(drifts []store.Drift, now time.Time, days int) LineChart {
	end := now.UTC().Truncate(24*time.Hour).AddDate(0, 0, 1)
	c := LineChart{}
	for d := range days {
		c.Labels = append(c.Labels, end.AddDate(0, 0, d-days).Format("Jan 2"))
	}
	top := 0
	for _, dk := range driftKinds {
		s := Series{Name: dk.label, Class: dk.class, Values: make([]int, days)}
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

// smoothPath is an SVG path through the points of values that bends smoothly
// without overshooting (monotone cubic interpolation, Fritsch–Carlson), so a curve
// never dips below zero or above a peak that the data does not have.
func smoothPath(values []int, top int) string {
	n := len(values)
	if n == 0 {
		return ""
	}
	xs, ys := make([]float64, n), make([]float64, n)
	for i, v := range values {
		xs[i], ys[i] = pointX(i, n), yAt(v, top)
	}
	if n == 1 {
		return "M" + f(xs[0]) + "," + f(ys[0])
	}
	// Slopes of the segments, then tangents at the points.
	d := make([]float64, n-1)
	for i := range n - 1 {
		d[i] = (ys[i+1] - ys[i]) / (xs[i+1] - xs[i])
	}
	m := make([]float64, n)
	m[0], m[n-1] = d[0], d[n-2]
	for i := 1; i < n-1; i++ {
		if d[i-1]*d[i] <= 0 {
			m[i] = 0 // a peak or a valley stays flat
		} else {
			m[i] = (d[i-1] + d[i]) / 2
		}
	}
	for i := range n - 1 {
		if d[i] == 0 {
			m[i], m[i+1] = 0, 0
			continue
		}
		a, b := m[i]/d[i], m[i+1]/d[i]
		if h := a*a + b*b; h > 9 {
			t := 3 / math.Sqrt(h)
			m[i], m[i+1] = t*a*d[i], t*b*d[i]
		}
	}
	var sb strings.Builder
	sb.WriteString("M" + f(xs[0]) + "," + f(ys[0]))
	for i := range n - 1 {
		dx := (xs[i+1] - xs[i]) / 3
		sb.WriteString(" C" + f(xs[i]+dx) + "," + f(ys[i]+m[i]*dx) + " " + f(xs[i+1]-dx) + "," + f(ys[i+1]-m[i+1]*dx) +
			" " + f(xs[i+1]) + "," + f(ys[i+1]))
	}
	return sb.String()
}

// areaPath closes smoothPath down to the x axis, for the gradient under a line.
func areaPath(values []int, top int) string {
	if len(values) == 0 {
		return ""
	}
	n := len(values)
	return smoothPath(values, top) + " L" + f(pointX(n-1, n)) + "," + f(chartH) + " L" + f(pointX(0, n)) + "," + f(chartH) + " Z"
}

// chartID is a stable id prefix for one chart's gradients, from its label.
func chartID(label string) string {
	h := fnv.New32a()
	_, _ = h.Write([]byte(label))
	return fmt.Sprintf("ch%x", h.Sum32())
}

// hoverData is what app.js needs to draw the crosshair and the tooltip: the x of each
// point (in viewBox units), its label, and every series' values and y coordinates.
func hoverData(labels []string, series []Series, top int, bars bool) string {
	type hs struct {
		Name   string    `json:"name"`
		Class  string    `json:"cls"`
		Values []int     `json:"values"`
		Ys     []float64 `json:"ys,omitempty"`
	}
	out := struct {
		W      float64   `json:"w"`
		Labels []string  `json:"labels"`
		Xs     []float64 `json:"xs"`
		Series []hs      `json:"series"`
		Bars   bool      `json:"bars,omitempty"`
	}{W: chartW, Labels: labels, Bars: bars}
	for i := range labels {
		if bars {
			x, w := barX(i, len(labels))
			out.Xs = append(out.Xs, math.Round((x+w/2)*10)/10)
		} else {
			out.Xs = append(out.Xs, math.Round(pointX(i, len(labels))*10)/10)
		}
	}
	for _, s := range series {
		if !anyNonZero(s.Values) {
			continue
		}
		h := hs{Name: s.Name, Class: s.Class, Values: s.Values}
		if !bars {
			for _, v := range s.Values {
				h.Ys = append(h.Ys, math.Round(yAt(v, top)*10)/10)
			}
		}
		out.Series = append(out.Series, h)
	}
	b, _ := json.Marshal(out)
	return string(b)
}

func sum(values []int) int {
	t := 0
	for _, v := range values {
		t += v
	}
	return t
}

func last(values []int) int {
	if len(values) == 0 {
		return 0
	}
	return values[len(values)-1]
}

// sparkPaths are the line and the filled area of a sparkline in a w × h box.
func sparkPaths(values []int, top int, w, h float64) (line, area string) {
	n := len(values)
	if n < 2 || top == 0 {
		return "", ""
	}
	pt := func(i int) (float64, float64) {
		return float64(i) / float64(n-1) * w, h - 2 - float64(values[i])/float64(top)*(h-6)
	}
	var sb strings.Builder
	x0, y0 := pt(0)
	sb.WriteString("M" + f(x0) + "," + f(y0))
	for i := 1; i < n; i++ {
		px, py := pt(i - 1)
		x, y := pt(i)
		mx := (px + x) / 2
		sb.WriteString(" C" + f(mx) + "," + f(py) + " " + f(mx) + "," + f(y) + " " + f(x) + "," + f(y))
	}
	line = sb.String()
	return line, line + " L" + f(w) + "," + f(h) + " L0," + f(h) + " Z"
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

// SegLabel is where a timeline segment's version is written: inside the bar when it fits, otherwise beside
// it where the lane is free, so a short or recent segment still says what it is.
type SegLabel struct {
	X       float64
	Anchor  string // start or end
	Outside bool
	Show    bool
}

func segLabel(lane Lane, i int, labelW float64) SegLabel {
	span := chartW - labelW
	sg := lane.Segments[i]
	x, w := labelW+sg.Start*span, max((sg.End-sg.Start)*span, 2)
	text := float64(len(sg.Version))*7 + 10
	if w > text {
		return SegLabel{X: x + 6, Anchor: "start", Show: true}
	}
	free := func(from, to float64) bool { // nothing else of the lane between from and to
		if from < labelW || to > chartW {
			return false
		}
		for j, o := range lane.Segments {
			ox, ow := labelW+o.Start*span, max((o.End-o.Start)*span, 2)
			if j != i && ox < to && ox+ow > from {
				return false
			}
		}
		return true
	}
	if free(x+w, x+w+text) {
		return SegLabel{X: x + w + 4, Anchor: "start", Outside: true, Show: true}
	}
	if free(x-text, x) {
		return SegLabel{X: x - 4, Anchor: "end", Outside: true, Show: true}
	}
	return SegLabel{}
}
