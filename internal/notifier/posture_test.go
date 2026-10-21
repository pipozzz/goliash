// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package notifier

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/pipozzz/goliash/internal/store"
	"github.com/pipozzz/goliash/internal/versions"
)

func TestPostureReport(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	ciso := newSink(t)
	everything := newSink(t)
	e.rule(e.channel("slack", "ciso", map[string]any{"url": ciso.srv.URL}), "weekly", []string{EventPostureReport}, Filter{})
	// A rule for every event does not get the report: it is asked for by name, like the plan.
	e.rule(e.channel("slack", "all", map[string]any{"url": everything.srv.URL}), "instant", nil, Filter{})
	if _, err := e.st.OpenDrift(ctx, store.Drift{
		Scope: e.sc, ServiceID: e.db.ID, EnvironmentID: e.prod.ID, Kind: "eol",
		Detail: json.RawMessage(`{"running":"13.4","other":"13","eol":"2025-11-13"}`),
	}); err != nil {
		t.Fatal(err)
	}

	e.clock = time.Date(2026, 10, 5, 9, 5, 0, 0, time.UTC) // Monday, after 8:00
	for range 2 {
		if err := e.n.PlanUpdates(ctx); err != nil {
			t.Fatal(err)
		}
		_ = e.n.DeliverDue(ctx)
	}
	if ciso.count() != 1 {
		t.Fatalf("report sent %d times", ciso.count())
	}
	msg := ciso.last()
	for _, want := range []string{"security posture", "prod runs", "End of life: 1 passed", "Accepted risk: 0", "postgres 13.4"} {
		if !strings.Contains(msg, want) {
			t.Errorf("report misses %q: %s", want, msg)
		}
	}
	if everything.count() > 0 && strings.Contains(everything.last(), "posture") {
		t.Error("a rule for every event got the report")
	}

	// Send now, for checking the channel; it shows under the bell.
	rules, _ := e.st.ListRules(ctx, e.sc)
	var report, other string
	for _, r := range rules {
		if contains(r.EventTypes, EventPostureReport) {
			report = r.ID
		} else {
			other = r.ID
		}
	}
	// With a reading from a week before, the report says where the numbers were.
	if err := e.st.RecordPosture(ctx, e.sc, store.PostureSample{Day: "2026-09-28", Exposed: 4, Median: 70, Behind90: 3, EOLPassed: 2}); err != nil {
		t.Fatal(err)
	}
	if err := e.n.SendPostureNow(ctx, e.sc, report); err != nil || ciso.count() != 2 {
		t.Fatalf("send now: %v, sent %d", err, ciso.count())
	}
	if !strings.Contains(ciso.last(), "A week ago (2026-09-28): 4 behind, median 70 days, 3 behind 90+, 2 past end of life.") {
		t.Errorf("no week-ago numbers: %s", ciso.last())
	}
	// And the hourly reading records today's.
	if err := e.n.RecordPostures(ctx); err != nil {
		t.Fatal(err)
	}
	if hist, _ := e.st.PostureHistory(ctx, e.sc, "2026-10-05"); len(hist) != 1 || hist[0].EOLPassed != 1 {
		t.Errorf("recorded %+v", hist)
	}
	if feed, _ := e.st.NotificationFeed(ctx, e.sc, time.Time{}, 10); len(feed) == 0 {
		t.Error("the report sent now is not under the bell")
	}
	if err := e.n.SendPostureNow(ctx, e.sc, other); !errors.Is(err, ErrNotAReportRule) {
		t.Errorf("not a report rule: %v", err)
	}
}

func TestPostureItemOutdatedBases(t *testing.T) {
	pos := versions.Posture{Prod: store.Environment{Name: "prod"}, ProdRunning: 9, OutdatedBase: []string{"api", "checkout", "web", "worker"}}
	it := PostureItem(pos, SupplyChain{}, nil, time.Now())
	if !strings.Contains(it.Text, "Built on an outdated base (rebuild with --pull): api, checkout, web, 1 more.") {
		t.Errorf("text: %s", it.Text)
	}
}

func TestPostureItemSupplyChain(t *testing.T) {
	pos := versions.Posture{Prod: store.Environment{Name: "prod"}, ProdRunning: 9}
	supply := SupplyChain{
		Vulns: versions.VulnReport{WithSBOM: 6, Exploited: 1, Fixable: 4, Findings: []versions.VulnFinding{
			{Key: "CVE-2021-44228", Exploited: &store.Exploited{CVE: "CVE-2021-44228"}}, {Key: "CVE-2024-1"}, {Key: "CVE-2024-2"},
		}},
		Licenses: versions.LicenseReport{WithSBOM: 6, Counts: map[versions.LicenseClass]int{versions.NetworkCopyleft: 1, versions.UnknownLicense: 2, versions.Permissive: 40}},
	}
	it := PostureItem(pos, supply, nil, time.Now())
	for _, want := range []string{
		"Known vulnerabilities in 6 images with an SBOM: 3, 1 exploited in the wild (CVE-2021-44228); 4 with a fix out.",
		"Licenses that need a look: 1 network copyleft, 2 unknown.",
	} {
		if !strings.Contains(it.Text, want) {
			t.Errorf("missing %q in %s", want, it.Text)
		}
	}
	if strings.Contains(PostureItem(pos, SupplyChain{}, nil, time.Now()).Text, "vulnerabilities") {
		t.Error("no SBOM, no vulnerability sentence")
	}
}

// The report says how the week's upgrades went out: opened, merged, running, the median from merge to running, and
// those merged and not deployed with why.
func TestPostureItemPulls(t *testing.T) {
	pos := versions.Posture{Prod: store.Environment{Name: "prod"}, ProdRunning: 9}
	now := time.Now()
	app := func(service string, manual bool) versions.GitOpsApp {
		return versions.GitOpsApp{Service: service, WorkloadGitOps: store.WorkloadGitOps{Tool: "argocd", ManualSync: manual}}
	}
	merged := func(to string) store.GitOpsPull {
		return store.GitOpsPull{State: "merged", To: to, Environment: "prod", MergedAt: now.Add(-3 * time.Hour)}
	}
	supply := SupplyChain{Pulls: []versions.PullTiming{
		{GitOpsPull: store.GitOpsPull{State: "open", To: "2.0.0"}, App: app("api", false), Known: true},
		{GitOpsPull: merged("1.5.0"), App: app("web", false), Known: true, Deployed: true, MergeToRunning: 20 * time.Minute},
		{GitOpsPull: merged("3.1.0"), App: app("payments", true), Known: true},
	}}
	it := PostureItem(pos, supply, nil, now)
	for _, want := range []string{
		"Upgrades as pull requests this week: 3 opened, 2 merged, 1 running, median 20m from merge to running.",
		"Merged, not deployed: payments 3.1.0 in prod (Argo CD syncs it by hand).",
	} {
		if !strings.Contains(it.Text, want) {
			t.Errorf("missing %q in %s", want, it.Text)
		}
	}
	if strings.Contains(PostureItem(pos, SupplyChain{}, nil, now).Text, "pull requests") {
		t.Error("no pull requests, no sentence")
	}
}

// The blind spots say why newest releases are unknown, the most common reason first.
func TestPostureItemUnknownReasons(t *testing.T) {
	pos := versions.Posture{Prod: store.Environment{Name: "prod"}, ProdRunning: 9, NoUpstream: []string{"a", "b", "c"},
		Unknowns: []versions.UnknownService{
			{Service: "a", Unknown: versions.Unknown{Short: "checked by agent"}},
			{Service: "b", Unknown: versions.Unknown{Short: "not a version"}},
			{Service: "c", Unknown: versions.Unknown{Short: "not a version"}},
		}}
	if it := PostureItem(pos, SupplyChain{}, nil, time.Now()); !strings.Contains(it.Text, "3 services without a known upstream (2 not a version, 1 checked by agent).") {
		t.Fatal(it.Text)
	}
}
