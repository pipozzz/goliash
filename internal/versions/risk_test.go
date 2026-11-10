// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package versions

import (
	"slices"
	"testing"
	"time"

	"github.com/pipozzz/goliash/internal/store"
)

func TestUpgradeRisk(t *testing.T) {
	svc, prod, staging := store.Service{ID: "api"}, store.Environment{ID: "prod"}, store.Environment{ID: "staging"}
	up := func(mod func(*Update)) Update {
		u := Update{Service: svc, Environment: prod, Running: "2.4.1", Target: "2.4.3", Jump: JumpPatch}
		mod(&u)
		return u
	}
	us := []Update{
		up(func(*Update) {}),
		up(func(u *Update) { u.Target, u.Jump = "3.0.0", JumpMajor }),
		up(func(u *Update) {
			u.Target, u.Jump, u.Breaking = "3.1.0", JumpMajor, []string{"3.0.0: Removed the v1 API"}
		}),
		up(func(u *Update) { u.Target, u.Jump = "3.2.0", JumpMajor }), // runs in staging already
		up(func(u *Update) {
			u.Target, u.Jump = "2.5.0", JumpMinor
			u.Vulns = VulnDelta{Known: true, New: []VulnRef{{ID: "CVE-2026-1", Exploited: true}}}
		}),
		{Service: svc, Environment: prod, Running: "2.4.1", Remedy: "rebuild"}, // no target: no risk
	}
	active := []store.Instance{
		{ServiceID: "api", EnvironmentID: staging.ID, IsMain: true, Image: "ghcr.io/acme/api:3.2.0"},
		{ServiceID: "api", EnvironmentID: prod.ID, IsMain: true, Image: "ghcr.io/acme/api:3.0.0"}, // its own environment: no proof
	}
	got := WithRisk(us, active)
	want := []string{RiskLow, RiskMedium, RiskHigh, RiskLow, RiskHigh, ""}
	for i, u := range got {
		if u.Risk != want[i] {
			t.Errorf("%s: %s (%v), want %s", u.Target, u.Risk, u.RiskReasons, want[i])
		}
	}
	if !slices.Contains(got[3].RiskReasons, "3.2.0 runs already elsewhere") {
		t.Errorf("reasons %v", got[3].RiskReasons)
	}
}

// What waits at the platform, with nothing newer to move to, is a line of its own: a reboot, or patches.
func TestWithMaintenance(t *testing.T) {
	o := Overview{
		Services: map[string]store.Service{"vm": {ID: "vm", Name: "ubuntu"}, "db": {ID: "db", Name: "billing-db"}, "api": {ID: "api", Name: "api"}},
		Envs:     map[string]store.Environment{"prod": {ID: "prod", Name: "prod"}},
	}
	active := []store.Instance{
		{ServiceID: "vm", EnvironmentID: "prod", IsMain: true, Image: "docker.io/library/ubuntu:24.04", Maintenance: []string{"reboot required: kernel 6.8.0-1015-aws installed, 6.8.0-1012-aws runs"}},
		{ServiceID: "db", EnvironmentID: "prod", IsMain: true, Image: "rds.amazonaws.com/postgres:16.4", Maintenance: []string{"system-update: New Operating System update is available"}},
		{ServiceID: "api", EnvironmentID: "prod", IsMain: true, Image: "ghcr.io/acme/api:1.0", Maintenance: []string{"reboot required"}},
	}
	listed := []Update{{Service: o.Services["api"], Environment: o.Envs["prod"], Running: "1.0", Target: "1.1"}} // has a line already
	got := WithMaintenance(listed, o, active, nil, time.Now())
	byName := map[string]Update{}
	for _, u := range got {
		byName[u.Service.Name] = u
	}
	if len(got) != 3 || !byName["ubuntu"].MaintenanceOnly || byName["ubuntu"].Remedy != "reboot" || byName["ubuntu"].Running != "24.04" {
		t.Fatalf("host: %+v", got)
	}
	if byName["billing-db"].Remedy != "apply the patches waiting at the platform" || byName["api"].MaintenanceOnly {
		t.Fatalf("others: %+v", got)
	}
}
