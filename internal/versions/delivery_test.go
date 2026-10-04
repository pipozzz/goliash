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
	deploys := map[string]int{}
	for _, e := range d.Envs {
		deploys[e.Env.Name] = e.Deploys
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
