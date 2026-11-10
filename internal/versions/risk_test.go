// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package versions

import (
	"slices"
	"testing"

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
