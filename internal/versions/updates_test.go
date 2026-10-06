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
