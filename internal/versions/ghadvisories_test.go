// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package versions

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pipozzz/goliash/internal/store"
)

// The ranges and patched versions as projects write them in their GitHub advisories.
func TestAffects(t *testing.T) {
	for _, c := range []struct {
		running, rng, patched string
		fix                   string
		affected              bool
	}{
		// Redis: every version, fixed on each release line.
		{"7.4.2", "All", "6.2.20, 7.2.11, 7.4.6, 8.0.4, 8.2.2", "7.4.6", true},
		{"7.4.6", "All", "6.2.20, 7.2.11, 7.4.6, 8.0.4, 8.2.2", "", false},
		{"7.0.15", "All", "6.2.20, 7.2.11, 7.4.6, 8.0.4, 8.2.2", "7.2.11", true},
		{"8.4.0", "All", "6.2.20, 7.2.11, 7.4.6, 8.0.4, 8.2.2", "", false},
		{"8.2.1", ">= 8.2.0", "8.2.3", "8.2.3", true},
		{"7.2.4", ">= 7.0.0", "TBD", "", true},
		{"6.2.1", ">= 7.0.0", "TBD", "", false},
		// Keycloak.
		{"26.4.10", "< 26.4.14", "26.4.14", "26.4.14", true},
		{"26.7.2", ">= 26.7.0, < 26.7.4", "26.7.4", "26.7.4", true},
		{"26.6.0", ">= 26.7.0, < 26.7.4", "26.7.4", "", false},
		// Traefik, with v prefixes and inclusive bounds.
		{"3.7.15", ">= v3.0.0, <= v3.7.15", "v3.7.16", "3.7.16", true},
		{"2.11.60", "<= v2.11.59", "v2.11.60", "", false},
		// Grafana: two intervals in one string, and a dash range.
		{"9.2.5", ">=9.2.0 <9.2.10 >=9.3.0 <9.3.4", "", "", true},
		{"9.3.5", ">=9.2.0 <9.2.10 >=9.3.0 <9.3.4", "", "", false},
		{"9.1.0", "6.3.0-beta1 - 9.3.1", "9.3.2, 9.2.8, 8.5.16", "9.2.8", true},
		// Envoy: patched on four lines; a newer line is fixed.
		{"1.39.1", ">= 1.33.0", "1.36.12 1.37.8 1.38.6 1.39.3", "1.39.3", true},
		{"1.40.0", ">= 1.33.0", "1.36.12 1.37.8 1.38.6 1.39.3", "", false},
		{"1.32.0", ">= 1.33.0", "1.36.12 1.37.8 1.38.6 1.39.3", "", false},
		// NATS: two branches as alternatives.
		{"2.12.5", "<= 2.14.2, <= 2.12.11", "2.14.3, 2.12.12", "2.12.12", true},
		{"0.4.10", "v0.4.15 and earlier, including main", "", "", true},
	} {
		v, _ := ParseVersion(c.running)
		fix, affected := affects(v, c.rng, c.patched)
		if affected != c.affected || fix != c.fix {
			t.Errorf("%s in %q / %q: affected %v fix %q, want %v %q", c.running, c.rng, c.patched, affected, fix, c.affected, c.fix)
		}
	}
}

func TestAdvisoryVulns(t *testing.T) {
	now := time.Date(2026, 10, 23, 0, 0, 0, 0, time.UTC)
	var list []GitHubAdvisory
	add := func(ghsa, cve, severity, rng, patched string, published time.Time) {
		a := GitHubAdvisory{GHSA: ghsa, CVE: cve, Summary: "about " + ghsa, Severity: severity, Published: published}
		a.Vulnerabilities = append(a.Vulnerabilities, struct {
			Range   string `json:"vulnerable_version_range"`
			Patched string `json:"patched_versions"`
		}{rng, patched})
		list = append(list, a)
	}
	add("GHSA-1", "CVE-2026-1", "critical", "All", "7.2.11, 7.4.6", now.AddDate(0, 0, -10))
	add("GHSA-2", "", "high", ">= 7.0.0", "TBD", now.AddDate(0, 0, -5))     // not fixed yet, recent: it counts
	add("GHSA-3", "CVE-2020-9", "high", ">=7.0", "", now.AddDate(-2, 0, 0)) // never filled in, open-ended, old: left out
	add("GHSA-4", "CVE-2026-4", "low", "< 7.4.0", "7.4.0", now)
	var got []string
	for _, v := range AdvisoryVulns(list, "7.4.2", now) {
		got = append(got, v.CVE+" "+v.Severity+" "+v.Fix)
	}
	if strings.Join(got, "; ") != "CVE-2026-1 CRITICAL 7.4.6; GHSA-2 HIGH " {
		t.Errorf("vulns %q", got)
	}
}

// Redis publishes its advisories on GitHub: they are its source, and NVD is not asked about it. PostgreSQL's
// repository has none: NVD is.
func TestProductAdvisoriesFirst(t *testing.T) {
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/redis/redis/security-advisories":
			_, _ = w.Write([]byte(`[{"ghsa_id":"GHSA-4789-qfc9-5f9q","cve_id":"CVE-2025-49844","summary":"Lua use-after-free","severity":"critical",
				"published_at":"2025-10-03T00:00:00Z","cvss_severities":{"cvss_v3":{"score":9.9}},
				"vulnerabilities":[{"vulnerable_version_range":"All","patched_versions":"6.2.20, 7.2.11, 7.4.6, 8.0.4, 8.2.2"}]}]`))
		default:
			_, _ = w.Write([]byte(`[]`))
		}
	}))
	defer gh.Close()
	var nvdCalls atomic.Int32
	nvd := nvdServer(t, &nvdCalls)
	defer nvd.Close()

	l := newLab(t)
	ctx := context.Background()
	l.checker = NewChecker(l.st, l.tags, slog.New(slog.NewTextHandler(io.Discard, nil)), time.Hour)
	client := NewGitHub("")
	client.BaseURL = gh.URL
	l.checker.SetGitHub(client)
	l.checker.SetNVD(&NVD{BaseURL: nvd.URL, APIKey: "k", HTTP: nvd.Client()})
	cache, _ := l.st.EnsureService(ctx, l.sc, "cache")
	db, _ := l.st.EnsureService(ctx, l.sc, "db")
	tgt := l.targets["prod-a"]
	if err := l.st.ApplySnapshot(ctx, store.SnapshotChanges{Scope: l.sc, TargetID: tgt.ID, SnapshotID: store.NewID(), At: time.Now(), Upsert: []store.Instance{
		{TargetID: tgt.ID, EnvironmentID: tgt.EnvironmentID, ServiceID: cache.ID, WorkloadID: "c", WorkloadName: "cache", ContainerName: "redis", Image: "redis:7.2.4", Tag: "7.2.4", Digest: "sha256:r", Running: 1, IsMain: true},
		{TargetID: tgt.ID, EnvironmentID: tgt.EnvironmentID, ServiceID: db.ID, WorkloadID: "d", WorkloadName: "db", ContainerName: "postgres", Image: "postgres:16.2", Tag: "16.2", Digest: "sha256:p", Running: 1, IsMain: true},
	}}); err != nil {
		t.Fatal(err)
	}
	if err := l.checker.CheckProductVulns(ctx, l.sc); err != nil {
		t.Fatal(err)
	}
	if nvdCalls.Load() != 1 { // postgresql only: redis has its own advisories
		t.Fatalf("NVD asked %d times", nvdCalls.Load())
	}
	r, err := LoadVulnReport(ctx, l.st, l.sc, "")
	if err != nil {
		t.Fatal(err)
	}
	var redis *VulnFinding
	for i, f := range r.Findings {
		if f.Key == "CVE-2025-49844" {
			redis = &r.Findings[i]
		}
	}
	if redis == nil || redis.Severity != "CRITICAL" || redis.Affected[0].Package != "redis 7.2.4" || redis.Affected[0].Fix != "7.2.11" {
		t.Fatalf("redis finding %+v in %+v", redis, r.Findings)
	}
	if len(r.Findings) != 3 { // and postgresql's two from NVD
		t.Errorf("findings %d", len(r.Findings))
	}
}
