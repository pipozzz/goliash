// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package ui

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/pipozzz/goliash/internal/store"
)

func TestNiceMax(t *testing.T) {
	for n, want := range map[int]int{0: 4, 3: 4, 4: 4, 5: 8, 9: 20, 21: 40, 41: 80, 81: 200} {
		if got := niceMax(n); got != want {
			t.Errorf("niceMax(%d) = %d, want %d", n, got, want)
		}
	}
}

func TestDeploysPerWeek(t *testing.T) {
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC) // a Wednesday
	envs := []store.Environment{{ID: "dev", Name: "dev"}, {ID: "prod", Name: "prod"}}
	evs := []store.Event{
		{Type: "deployed", EnvironmentID: "dev", At: now.Add(-time.Hour)},          // this week
		{Type: "version_changed", EnvironmentID: "dev", At: now.AddDate(0, 0, -8)}, // last week
		{Type: "version_changed", EnvironmentID: "prod", At: now.AddDate(0, 0, -2)},
		{Type: "new_release", EnvironmentID: "prod", At: now},                 // not a deploy
		{Type: "deployed", EnvironmentID: "dev", At: now.AddDate(0, 0, -100)}, // too old
		{Type: "deployed", EnvironmentID: "gone", At: now},                    // unknown env
	}
	c := deploysPerWeek(evs, envs, now, 4)
	if len(c.Labels) != 4 || c.Labels[3] != "Oct 5" {
		t.Fatalf("labels %v", c.Labels)
	}
	if got := c.Series[0].Values; got[3] != 1 || got[2] != 1 {
		t.Fatalf("dev %v", got)
	}
	if got := c.Series[1].Values; got[3] != 1 || got[0]+got[1]+got[2] != 0 {
		t.Fatalf("prod %v", got)
	}
}

func TestOpenDriftPerDay(t *testing.T) {
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	drifts := []store.Drift{
		{Kind: "env", Since: now.AddDate(0, 0, -3)},
		{Kind: "env", Since: now.AddDate(0, 0, -5), ResolvedAt: now.AddDate(0, 0, -1)},
		{Kind: "upstream", Since: now.AddDate(0, 0, -40)},
	}
	c := openDriftPerDay(drifts, now, 7)
	env, up := c.Series[0].Values, c.Series[1].Values
	if env[6] != 1 || env[4] != 2 || env[0] != 0 {
		t.Fatalf("env %v", env)
	}
	for _, v := range up {
		if v != 1 {
			t.Fatalf("upstream %v", up)
		}
	}
	if len(nonZeroSeries(c.Series)) != 2 {
		t.Fatal("empty kinds in the legend")
	}
}

func TestVersionTimeline(t *testing.T) {
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	day := 24 * time.Hour
	envs := []store.Environment{{ID: "stg", Name: "staging"}, {ID: "prod", Name: "prod"}}
	evs := []store.Event{
		{Type: "deployed", EnvironmentID: "stg", TargetID: "s", ToVersion: "1.0", At: now.Add(-40 * day)},
		{Type: "version_changed", EnvironmentID: "stg", TargetID: "s", ToVersion: "1.1", At: now.Add(-15 * day)},
		{Type: "deployed", EnvironmentID: "prod", TargetID: "eu", ToVersion: "1.0", At: now.Add(-10 * day)},
		{Type: "removed", EnvironmentID: "prod", TargetID: "eu", At: now.Add(-5 * day)},
		{Type: "deployed", EnvironmentID: "prod", TargetID: "us", ToVersion: "0.9", At: now.Add(-20 * day)},
	}
	tl := versionTimeline(evs, envs, map[string]string{"s": "stg-1", "eu": "prod-eu", "us": "prod-us"}, now, 30*day)
	if len(tl.Lanes) != 3 || tl.Lanes[0].Env != "staging" || tl.Lanes[1].Env != "prod · prod-eu" || tl.Lanes[2].Env != "prod · prod-us" {
		t.Fatalf("lanes %+v", tl.Lanes)
	}
	stg := tl.Lanes[0].Segments
	if len(stg) != 2 || stg[0].Start != 0 || stg[0].Version != "1.0" || stg[1].End != 1 {
		t.Fatalf("staging %+v", stg)
	}
	eu := tl.Lanes[1].Segments
	if len(eu) != 1 || eu[0].End > 0.84 || eu[0].End < 0.82 {
		t.Fatalf("removed did not end the segment: %+v", eu)
	}
	if len(tl.Ticks) != 5 || tl.Ticks[4].Label != "Oct 7" {
		t.Fatalf("ticks %+v", tl.Ticks)
	}
}

// The smooth curve bends without overshooting: every control point stays between
// the two points it joins, so a curve never dips below zero or above a peak.
func TestSmoothPathMonotone(t *testing.T) {
	values := []int{0, 0, 3, 3, 10, 2, 2, 0, 7}
	top := 12
	d := smoothPath(values, top)
	if !strings.HasPrefix(d, "M") || strings.Count(d, "C") != len(values)-1 {
		t.Fatalf("path %s", d)
	}
	segs := strings.Split(strings.TrimPrefix(d, "M"), " C")
	for i, seg := range segs[1:] {
		lo, hi := math.Min(yAt(values[i], top), yAt(values[i+1], top)), math.Max(yAt(values[i], top), yAt(values[i+1], top))
		for _, p := range strings.Fields(seg)[:2] {
			var x, y float64
			if _, err := fmt.Sscanf(p, "%f,%f", &x, &y); err != nil {
				t.Fatal(err)
			}
			if y < lo-0.05 || y > hi+0.05 {
				t.Errorf("segment %d overshoots: control y %.1f outside [%.1f, %.1f]", i, y, lo, hi)
			}
		}
	}
	if a := areaPath(values, top); !strings.HasSuffix(a, " Z") || !strings.Contains(a, f(chartH)) {
		t.Errorf("area %s", a)
	}
	if smoothPath(nil, 4) != "" || areaPath(nil, 4) != "" {
		t.Error("empty series draws something")
	}
}

func TestHoverData(t *testing.T) {
	s := hoverData([]string{"Sep 1", "Sep 2"}, []Series{{Name: "prod", Class: "c0", Values: []int{1, 3}}, {Name: "none", Class: "c1", Values: []int{0, 0}}}, 4, false)
	var d struct {
		Xs     []float64
		Labels []string
		Series []struct {
			Name string
			Ys   []float64
		}
	}
	if err := json.Unmarshal([]byte(s), &d); err != nil {
		t.Fatal(err)
	}
	if len(d.Xs) != 2 || len(d.Series) != 1 || d.Series[0].Name != "prod" || len(d.Series[0].Ys) != 2 || d.Series[0].Ys[1] >= d.Series[0].Ys[0] {
		t.Errorf("hover data %s", s)
	}
	line, area := sparkPaths([]int{0, 2, 0, 5}, 5, 150, 40)
	if !strings.HasPrefix(line, "M0.0,") || !strings.HasSuffix(area, " Z") {
		t.Errorf("spark %q %q", line, area)
	}
	if l, _ := sparkPaths([]int{0, 0}, 0, 150, 40); l != "" {
		t.Error("an empty sparkline draws a line")
	}
}
