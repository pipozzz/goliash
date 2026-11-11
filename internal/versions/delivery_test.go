// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package versions

import (
	"context"
	"testing"
	"time"

	"github.com/pipozzz/goliash/internal/store"
)

func TestDeliveryStats(t *testing.T) {
	l := newLab(t)
	ctx := context.Background()
	l.run(map[string]string{"stg-1": "1.28.0", "prod-a": "1.28.0"})
	now := time.Now().UTC()
	t0 := now.Add(-20 * 24 * time.Hour)
	add := func(typ, env, from, to string, at time.Time) {
		t.Helper()
		if err := l.st.InsertEvent(ctx, l.sc, store.Event{
			Type: typ, ServiceID: l.svc.ID, EnvironmentID: l.envs[env].ID, FromVersion: from, ToVersion: to, Source: "poll", At: at,
		}); err != nil {
			t.Fatal(err)
		}
	}
	add("version_changed", "staging", "1.26.2", "1.27.2", t0)
	add("version_changed", "prod", "1.26.2", "1.27.2", t0.Add(48*time.Hour))
	add("version_changed", "prod", "1.26.2", "1.27.2", t0.Add(50*time.Hour)) // a second prod target: same arrival
	add("version_changed", "staging", "1.27.2", "1.28.0", t0.Add(5*24*time.Hour))
	add("version_changed", "prod", "1.27.2", "1.28.0", t0.Add(6*24*time.Hour))
	add("version_changed", "prod", "1.28.0", "1.28.0", t0.Add(7*24*time.Hour)) // retag
	add("version_changed", "staging", "1.25.0", "1.26.2", now.Add(-60*24*time.Hour))
	add("version_changed", "prod", "1.28.0", "1.27.2", t0.Add(8*24*time.Hour))     // rolled back: not a new arrival
	add("version_changed", "prod", "1.27.2", "1.28.0", t0.Add(9*24*time.Hour))     // and forward again
	add("version_changed", "staging", "2.0.0", "1.9.0", now.Add(-40*24*time.Hour)) // a rollback before the window

	o, err := LoadOverview(ctx, l.st, l.sc)
	if err != nil {
		t.Fatal(err)
	}
	stats, err := DeliveryStats(ctx, l.st, l.sc, o, 30*24*time.Hour, now)
	if err != nil {
		t.Fatal(err)
	}
	var d Delivery
	for _, x := range stats {
		if x.Service.ID == l.svc.ID {
			d = x
		}
	}
	deploys, rollbacks := map[string]int{}, map[string]int{}
	for _, e := range d.Envs {
		deploys[e.Env.Name], rollbacks[e.Env.Name] = e.Deploys, e.Rollbacks
	}
	if rollbacks["prod"] != 1 || rollbacks["staging"] != 0 {
		t.Fatalf("rollbacks %v", rollbacks)
	}
	for _, e := range d.Envs {
		if e.Env.Name == "prod" && !e.LastDeploy.Equal(t0.Add(9*24*time.Hour)) {
			t.Errorf("prod's last deploy %v, want the redeploy after the rollback", e.LastDeploy)
		}
	}
	if deploys["staging"] != 2 || deploys["prod"] != 2 || deploys["dev"] != 0 {
		t.Fatalf("deploys %v (the arrival 60 days ago is outside the window, the retag is not a deploy)", deploys)
	}
	var lead LeadTime
	for _, lt := range d.LeadTimes {
		if lt.From.Name == "staging" && lt.To.Name == "prod" {
			lead = lt
		}
	}
	if lead.Samples != 2 || lead.Median != 36*time.Hour {
		t.Fatalf("staging → prod lead time %+v, want the median of 48h and 24h", lead)
	}
}

func TestIsRollback(t *testing.T) {
	for _, c := range []struct {
		from, to string
		want     bool
	}{
		{"1.28.0", "1.27.2", true},
		{"v2.0.0", "1.9.9", true},
		{"1.28.0", "1.28.0-rc.1", true},
		{"1.27.2", "1.28.0", false},
		{"1.28.0", "1.28.0", false},
		{"latest", "1.0.0", false},
		{"1.0.0", "sha-1a2b3c", false},
		{"", "1.0.0", false},
	} {
		if got := IsRollback(c.from, c.to); got != c.want {
			t.Errorf("IsRollback(%q, %q) = %v", c.from, c.to, got)
		}
	}
	if n := EventNote(store.Event{Type: "version_changed", FromVersion: "1.28.0", ToVersion: "1.27.2"}); n != RollbackNote {
		t.Errorf("note %q", n)
	}
	if n := EventNote(store.Event{Type: "version_changed", FromVersion: "1.28.0", ToVersion: "1.28.0", Note: "retag"}); n != "retag" {
		t.Errorf("retag note %q", n)
	}
}

func TestFlapBack(t *testing.T) {
	at := time.Date(2026, 10, 29, 10, 0, 0, 0, time.UTC)
	ev := func(from, to string, minutes int) store.Event {
		return store.Event{Type: "version_changed", ServiceID: "s", EnvironmentID: "prod", FromVersion: from, ToVersion: to, At: at.Add(time.Duration(minutes) * time.Minute)}
	}
	flap := []store.Event{ev("1.176.0", "1.177.0", 0), ev("1.177.0", "1.176.0", 1), ev("1.176.0", "1.177.0", 2)}
	if !flapBack(flap, 1) {
		t.Error("a rollback undone a minute later counts")
	}
	fixed := []store.Event{ev("1.176.0", "1.177.0", 0), ev("1.177.0", "1.176.0", 10), ev("1.176.0", "1.177.0", 300)}
	if flapBack(fixed, 1) {
		t.Error("a rollback fixed forward hours later does not count")
	}
}
