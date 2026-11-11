// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package versions

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/pipozzz/goliash/internal/store"
)

func TestUpdates(t *testing.T) {
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	stg, prod := store.Environment{ID: "stg", Name: "staging", Position: 20}, store.Environment{ID: "prod", Name: "prod", Position: 30}
	svcs := map[string]store.Service{
		"pg": {ID: "pg", Name: "postgres"}, "web": {ID: "web", Name: "web"}, "kc": {ID: "kc", Name: "keycloak"}, "cache": {ID: "cache", Name: "redis"},
	}
	drift := func(svc, app, env, kind string, d DriftDetail, daysAgo int) store.Drift {
		b, _ := json.Marshal(d)
		return store.Drift{ServiceID: svc, App: app, EnvironmentID: env, Kind: kind, Detail: b, Since: now.AddDate(0, 0, -daysAgo)}
	}
	o := Overview{
		Services: svcs, Envs: map[string]store.Environment{"stg": stg, "prod": prod},
		Upstreams:  map[string]Upstream{"pg": {Latest: mustVersion(t, "15.8"), HasLatest: true}},
		ReleaseURL: map[string]string{"kc|26.0.0": "https://example.com/kc-26"},
		Drifts: map[string][]store.Drift{
			"pg|prod": {
				drift("pg", "auth", "prod", "eol", DriftDetail{Running: "15.6", Other: "15", EOL: "2026-09-01"}, 30),
				drift("pg", "auth", "prod", "env", DriftDetail{Running: "15.6", Other: "15.7", OtherIn: "staging", Jump: JumpPatch}, 3),
			},
			"kc|prod":    {drift("kc", "", "prod", "upstream", DriftDetail{Running: "25.0.6", Other: "26.0.0", Jump: JumpMajor}, 10)},
			"kc|stg":     {drift("kc", "", "stg", "upstream", DriftDetail{Running: "25.0.6", Other: "26.0.0", Jump: JumpMajor}, 12)},
			"web|prod":   {drift("web", "", "prod", "inconsistent", DriftDetail{Running: "1.0"}, 1)}, // not an upgrade
			"cache|prod": {drift("cache", "", "prod", "upstream", DriftDetail{Running: "7.2.4", Other: "7.2.5", Jump: JumpPatch}, 2)},
		},
	}
	acks := []store.Ack{{ServiceID: "cache", Kind: "drift", UntilVersion: "7.3.0"}}
	us := Updates(o, acks, now)
	var got []string
	for _, u := range us {
		got = append(got, u.Service.Name+"/"+u.App+"@"+u.Environment.Name+":"+u.Running+"->"+u.Target+":"+UrgencyLabels[u.Urgency])
	}
	want := "postgres/auth@prod:15.6->15.7:end of life keycloak/@prod:25.0.6->26.0.0:major keycloak/@staging:25.0.6->26.0.0:major redis/@prod:7.2.4->7.2.5:patch"
	if strings.Join(got, " ") != want {
		t.Fatalf("updates\n got %s\nwant %s", strings.Join(got, " "), want)
	}
	if us[0].Behind != "staging" || !us[0].EOLPassed || !us[0].Since.Equal(now.AddDate(0, 0, -30)) {
		t.Errorf("postgres %+v", us[0])
	}
	if us[1].TargetURL != "https://example.com/kc-26" {
		t.Errorf("release notes %q", us[1].TargetURL)
	}
	if !us[3].Acked || us[1].Acked {
		t.Errorf("acks: redis %v keycloak %v", us[3].Acked, us[1].Acked)
	}
}

func mustVersion(t *testing.T, s string) Version {
	t.Helper()
	v, ok := ParseVersion(s)
	if !ok {
		t.Fatal(s)
	}
	return v
}

// An update whose running image has a vulnerability exploited in the wild goes first, with it named.
func TestUpdatesWithVulnerabilities(t *testing.T) {
	prod := store.Environment{ID: "e-prod", Name: "prod", Position: 30}
	us := []Update{
		{Service: store.Service{ID: "s-db", Name: "db"}, Environment: prod, Running: "15.6", Target: "15.8", EOL: "2026-01-01", EOLPassed: true},
		{Service: store.Service{ID: "s-pay", Name: "payments"}, Environment: prod, Running: "2.0.0", Target: "2.0.1", Jump: JumpPatch},
	}
	for i := range us {
		us[i].Urgency = urgency(us[i])
	}
	r := VulnReport{Findings: []VulnFinding{
		{Key: "CVE-2021-44228", Exploited: &store.Exploited{}, Affected: []Affected{{ServiceID: "s-pay", EnvironmentID: "e-prod"}, {ServiceID: "s-pay", EnvironmentID: "e-prod"}}},
		{Key: "CVE-2024-0001", Severity: "CRITICAL", Affected: []Affected{{ServiceID: "s-db", EnvironmentID: "e-prod"}}},
		{Key: "CVE-2024-0002", Severity: "LOW", Affected: []Affected{{ServiceID: "s-db", EnvironmentID: "e-prod"}}},
	}}
	got := WithVulnerabilities(us, r)
	if got[0].Service.Name != "payments" || got[0].Urgency != UrgencyExploited || len(got[0].Exploited) != 1 || got[0].Exploited[0] != "CVE-2021-44228" {
		t.Fatalf("first %+v", got[0])
	}
	if got[1].Service.Name != "db" || got[1].Critical != 1 || got[1].Urgency != UrgencyEOLPassed {
		t.Fatalf("second %+v", got[1])
	}
	if UrgencyLabels[UrgencyExploited] != "exploited vulnerability" || len(UrgencyLabels) != UrgencyPatch+1 {
		t.Fatal("labels out of step with the levels")
	}
}

// What runs an exploited vulnerability with no upgrade on the list is on it anyway, with what fixes it; a service
// already on it, a mitigated one, and one whose vulnerability is only critical are not added.
func TestWithUnpatched(t *testing.T) {
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	prod := store.Environment{ID: "prod", Name: "production"}
	o := Overview{
		Services: map[string]store.Service{"lq": {ID: "lq", Name: "leadqc"}, "px": {ID: "px", Name: "proxysql"}, "mt": {ID: "mt", Name: "mitigated"}},
		Envs:     map[string]store.Environment{"prod": prod},
	}
	us := []Update{{Service: o.Services["px"], Environment: prod, Running: "2.7.2", Target: "3.0.5"}}
	kev := &store.Exploited{DateAdded: "2026-01-02"}
	r := VulnReport{Findings: []VulnFinding{
		{Key: "CVE-2025-1", Exploited: kev, Affected: []Affected{
			{ServiceID: "lq", EnvironmentID: "prod", Image: "1.dkr.ecr.eu-central-1.amazonaws.com/leadqc:4.2.0", Package: "openssl", Fix: "3.0.15", Since: now.AddDate(0, 0, -9)},
			{ServiceID: "px", EnvironmentID: "prod", Image: "proxysql/proxysql:2.7.2"},
			{ServiceID: "mt", EnvironmentID: "prod", Image: "acme/mt:1", Mitigation: &store.Mitigation{}},
		}},
		{Key: "CVE-2025-2", Exploited: kev, Affected: []Affected{
			{ServiceID: "lq", EnvironmentID: "prod", Image: "1.dkr.ecr.eu-central-1.amazonaws.com/leadqc:4.2.0", Package: "zlib", Rebuild: "node:22-alpine"},
		}},
		{Key: "CVE-2025-3", Severity: "CRITICAL", Affected: []Affected{{ServiceID: "mt", EnvironmentID: "prod", Image: "acme/mt:1"}}},
	}}
	got := WithVulnerabilities(WithUnpatched(us, o, r, nil, now), r)
	if len(got) != 2 {
		t.Fatalf("updates %+v", got)
	}
	var lq Update
	for _, u := range got {
		if u.Service.ID == "lq" {
			lq = u
		}
	}
	if lq.Running != "4.2.0" || lq.Target != "" || lq.Remedy != "rebuild on the current node:22-alpine" ||
		lq.Urgency != UrgencyExploited || len(lq.Exploited) != 2 || !lq.Since.Equal(now.AddDate(0, 0, -9)) {
		t.Fatalf("leadqc %+v", lq)
	}
}

// An end of life to come, with the newest release running already (through a moving tag): nothing to upgrade to.
func TestUpdatesEOLOnNewest(t *testing.T) {
	now := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	prod := store.Environment{ID: "prod", Name: "prod", Position: 30}
	mb := store.Service{ID: "mb", Name: "metabase"}
	cell := func(tag, resolved string) []Cell {
		return []Cell{{Versions: []RunningVersion{{Tag: tag, Resolved: resolved}}}}
	}
	eol := func(date string) map[string][]store.Drift {
		b, _ := json.Marshal(DriftDetail{Running: "v0.65.x", Other: "0.65", EOL: date})
		return map[string][]store.Drift{"mb|prod": {{ServiceID: "mb", EnvironmentID: "prod", Kind: "eol", Detail: b, Since: now}}}
	}
	o := Overview{
		Services: map[string]store.Service{"mb": mb}, Envs: map[string]store.Environment{"prod": prod},
		Upstreams: map[string]Upstream{"mb": {Latest: mustVersion(t, "v0.65.3.3"), HasLatest: true}},
		Matrix:    Matrix{Environments: []store.Environment{prod}, Rows: []Row{{Service: mb, Cells: cell("v0.65.x", "v0.65.3.3")}}},
		Drifts:    eol("2027-01-01"),
	}
	if us := Updates(o, nil, now); len(us) != 0 {
		t.Fatalf("an upgrade to what runs: %+v", us)
	}
	o.Drifts = eol("2026-11-01") // passed: still listed, with nothing to move to named
	if us := Updates(o, nil, now); len(us) != 1 || us[0].Target != "" || !us[0].EOLPassed {
		t.Fatalf("passed end of life %+v", us)
	}
	o.Drifts = eol("2027-01-01")
	o.Matrix.Rows[0].Cells = cell("v0.65.2", "")
	if us := Updates(o, nil, now); len(us) != 1 || us[0].Target != "v0.65.3.3" {
		t.Fatalf("older running %+v", us)
	}
}
