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

// nvdServer answers like NVD's CVE API for postgresql 16.2: one CVE fixed in 16.3 (a range per major), one rated
// by the CNA only.
func nvdServer(t *testing.T, calls *atomic.Int32) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if !strings.HasPrefix(r.URL.Query().Get("virtualMatchString"), "cpe:2.3:a:postgresql:postgresql:16.2") {
			_, _ = w.Write([]byte(`{"totalResults":0,"vulnerabilities":[]}`))
			return
		}
		_, _ = w.Write([]byte(`{"totalResults":2,"vulnerabilities":[
		{"cve":{"id":"CVE-2024-4317","descriptions":[{"lang":"en","value":"Missing authorization in pg_stats_ext."}],
		  "metrics":{"cvssMetricV31":[{"type":"Secondary","cvssData":{"baseScore":3.1,"baseSeverity":"LOW"}},{"type":"Primary","cvssData":{"baseScore":4.3,"baseSeverity":"MEDIUM"}}]},
		  "configurations":[{"nodes":[{"cpeMatch":[
		    {"vulnerable":true,"criteria":"cpe:2.3:a:postgresql:postgresql:*:*:*:*:*:*:*:*","versionStartIncluding":"15.0","versionEndExcluding":"15.7"},
		    {"vulnerable":true,"criteria":"cpe:2.3:a:postgresql:postgresql:*:*:*:*:*:*:*:*","versionStartIncluding":"16.0","versionEndExcluding":"16.3"}]}]}]}},
		{"cve":{"id":"CVE-2024-10979","descriptions":[{"lang":"en","value":"PL/Perl environment variable changes."}],
		  "metrics":{"cvssMetricV31":[{"type":"Secondary","cvssData":{"baseScore":8.8,"baseSeverity":"HIGH"}}]},
		  "configurations":[]}}]}`))
	}))
}

// A service running postgres:16.2 gets NVD's CVEs for PostgreSQL 16.2 as findings, without any SBOM, and NVD is
// asked once a day.
func TestProductVulns(t *testing.T) {
	var calls atomic.Int32
	srv := nvdServer(t, &calls)
	defer srv.Close()
	l := newLab(t)
	ctx := context.Background()
	l.checker = NewChecker(l.st, l.tags, slog.New(slog.NewTextHandler(io.Discard, nil)), time.Hour)
	l.checker.SetNVD(&NVD{BaseURL: srv.URL, APIKey: "k", HTTP: srv.Client()})
	db, _ := l.st.EnsureService(ctx, l.sc, "postgres")
	tgt := l.targets["prod-a"]
	if err := l.st.ApplySnapshot(ctx, store.SnapshotChanges{Scope: l.sc, TargetID: tgt.ID, SnapshotID: store.NewID(), At: time.Now(), Upsert: []store.Instance{{
		TargetID: tgt.ID, EnvironmentID: tgt.EnvironmentID, ServiceID: db.ID, WorkloadID: "db", WorkloadName: "db", ContainerName: "postgres",
		Image: "postgres:16.2-alpine", Tag: "16.2-alpine", Digest: "sha256:pg", Running: 1, IsMain: true,
	}}}); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := l.checker.CheckProductVulns(ctx, l.sc); err != nil {
			t.Fatal(err)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("NVD asked %d times", calls.Load())
	}
	r, err := LoadVulnReport(ctx, l.st, l.sc, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Findings) != 2 {
		t.Fatalf("findings %+v", r.Findings)
	}
	// The high one first; the fixed version from the 16.x range; NVD's own score over the CNA's.
	high, medium := r.Findings[0], r.Findings[1]
	if high.Key != "CVE-2024-10979" || high.Severity != "HIGH" || high.Affected[0].Package != "postgresql 16.2" || high.Affected[0].Fix != "" {
		t.Errorf("high %+v", high)
	}
	if medium.Key != "CVE-2024-4317" || medium.Severity != "MEDIUM" || medium.Affected[0].Fix != "16.3" || medium.Affected[0].Service != "postgres" {
		t.Errorf("medium %+v", medium)
	}
	// CISA listing it makes it exploited, as for any finding.
	_ = l.st.SetKnownExploited(ctx, []store.Exploited{{CVE: "CVE-2024-4317", DueDate: "2026-11-01"}})
	if r, _ = LoadVulnReport(ctx, l.st, l.sc, ""); r.Findings[0].Key != "CVE-2024-4317" || r.Exploited != 1 {
		t.Errorf("exploited first: %+v", r.Findings[0])
	}
}

func TestProductVersion(t *testing.T) {
	for tag, want := range map[string]string{"16.2-alpine": "16.2", "v1.11.2": "1.11.2", "16": "", "latest": "", "24.0.5": "24.0.5"} {
		if got := productVersion(store.Instance{Image: "x:" + tag, Tag: tag}, nil); got != want {
			t.Errorf("%s: %q, want %q", tag, got, want)
		}
	}
	// A moving tag resolved to its version.
	in := store.Instance{Image: "postgres:16", Tag: "16", Digest: "sha256:d"}
	if got := productVersion(in, map[string]string{"docker.io/library/postgres@sha256:d": "16.4"}); got != "16.4" {
		t.Errorf("resolved: %q", got)
	}
}

func TestParseComponents(t *testing.T) {
	var got []string
	for _, u := range ParseComponents("nginx=1.27.2, php=v8.3.12; openssl=3.3 unknown=1.0 f5:nginx_open_source=1.26.0 broken redis=7") {
		got = append(got, u.CPE+"@"+u.Version+" ("+u.Name+")")
	}
	want := "f5:nginx@1.27.2 (nginx) f5:nginx_open_source@1.27.2 (nginx) php:php@8.3.12 (php) openssl:openssl@3.3 (openssl) f5:nginx_open_source@1.26.0 (nginx)"
	if strings.Join(got, " ") != want {
		t.Errorf("components:\n%s\nwant\n%s", strings.Join(got, " "), want)
	}
}

// An image built from ubuntu with nginx compiled from source declares it in its label: NVD's CVEs for that nginx
// show for the service.
func TestDeclaredComponents(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Query().Get("virtualMatchString") != "cpe:2.3:a:f5:nginx_open_source:1.25.3" {
			_, _ = w.Write([]byte(`{"totalResults":0,"vulnerabilities":[]}`))
			return
		}
		_, _ = w.Write([]byte(`{"totalResults":1,"vulnerabilities":[{"cve":{"id":"CVE-2024-7347","descriptions":[{"lang":"en","value":"ngx_http_mp4_module over-read."}],
		  "metrics":{"cvssMetricV31":[{"type":"Secondary","cvssData":{"baseScore":5.7,"baseSeverity":"MEDIUM"}}]},
		  "configurations":[{"nodes":[{"cpeMatch":[{"vulnerable":true,"criteria":"cpe:2.3:a:f5:nginx_open_source:*:*:*:*:*:*:*:*","versionStartIncluding":"1.5.13","versionEndExcluding":"1.27.1"}]}]}]}}]}`))
	}))
	defer srv.Close()
	l := newLab(t)
	ctx := context.Background()
	l.checker = NewChecker(l.st, l.tags, slog.New(slog.NewTextHandler(io.Discard, nil)), time.Hour)
	l.checker.SetNVD(&NVD{BaseURL: srv.URL, APIKey: "k", HTTP: srv.Client()})
	web, _ := l.st.EnsureService(ctx, l.sc, "web")
	tgt := l.targets["prod-a"]
	if err := l.st.ApplySnapshot(ctx, store.SnapshotChanges{Scope: l.sc, TargetID: tgt.ID, SnapshotID: store.NewID(), At: time.Now(), Upsert: []store.Instance{{
		TargetID: tgt.ID, EnvironmentID: tgt.EnvironmentID, ServiceID: web.ID, WorkloadID: "web", WorkloadName: "web", ContainerName: "web",
		Image: "ghcr.io/acme/web:4.1", Tag: "4.1", Digest: "sha256:web", Running: 1, IsMain: true,
	}}}); err != nil {
		t.Fatal(err)
	}
	if err := l.st.SetImageEvidence(ctx, l.sc, store.ImageEvidence{Repo: "ghcr.io/acme/web", Digest: "sha256:web", Components: "nginx=1.25.3"}); err != nil {
		t.Fatal(err)
	}
	if err := l.checker.CheckProductVulns(ctx, l.sc); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 { // nginx is two NVD products
		t.Fatalf("NVD asked %d times", calls.Load())
	}
	r, err := LoadVulnReport(ctx, l.st, l.sc, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Findings) != 1 || r.Findings[0].Key != "CVE-2024-7347" || len(r.Findings[0].Affected) != 1 ||
		r.Findings[0].Affected[0].Package != "nginx 1.25.3" || r.Findings[0].Affected[0].Fix != "1.27.1" || r.Findings[0].Affected[0].Service != "web" {
		t.Fatalf("findings %+v", r.Findings)
	}
}
