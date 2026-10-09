// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package versions

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pipozzz/goliash/internal/registry"
	"github.com/pipozzz/goliash/internal/store"
)

type sbomTags struct {
	*fakeTags
	reads atomic.Int32
}

func (s *sbomTags) ImageSBOM(_ context.Context, _, _ string, _ registry.Credentials) ([]string, error) {
	s.reads.Add(1)
	return []string{
		"pkg:deb/debian/openssl@3.0.11-1~deb12u2?os_distro=bookworm&os_name=debian&os_version=12",
		"pkg:npm/lodash@4.17.15",
	}, nil
}

func TestOSVQuery(t *testing.T) {
	for purl, want := range map[string]string{
		"pkg:deb/debian/openssl@3.0.11-1~deb12u2?os_distro=bookworm&os_name=debian&os_version=12": "Debian:12 openssl 3.0.11-1~deb12u2",
		"pkg:deb/ubuntu/libxml2@2.9.13%2Bdfsg-1ubuntu0.4?os_version=22.04":                        "Ubuntu:22.04 libxml2 2.9.13+dfsg-1ubuntu0.4",
		"pkg:apk/alpine/busybox@1.36.1-r29?os_version=3.20.3":                                     "Alpine:v3.20 busybox 1.36.1-r29",
		"pkg:npm/lodash@4.17.15":                          "purl pkg:npm/lodash@4.17.15",
		"pkg:golang/golang.org/x/net@v0.17.0?type=module": "purl pkg:golang/golang.org/x/net@v0.17.0",
	} {
		q, ok := osvQueryFor(purl)
		got := q.Package.Ecosystem + " " + q.Package.Name + " " + q.Version
		if q.Package.Purl != "" {
			got = "purl " + q.Package.Purl
		}
		if !ok || got != want {
			t.Errorf("%s: %q (%v), want %q", purl, got, ok, want)
		}
	}
	if _, ok := osvQueryFor("pkg:deb/debian/x@1"); ok {
		t.Error("a deb package without a distribution version")
	}
}

func TestCheckVulnerabilities(t *testing.T) {
	var batches atomic.Int32
	osv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/querybatch":
			batches.Add(1)
			var req struct {
				Queries []osvQuery `json:"queries"`
			}
			_ = json.NewDecoder(r.Body).Decode(&req)
			var results []map[string]any
			for _, q := range req.Queries {
				if q.Package.Name == "openssl" {
					results = append(results, map[string]any{"vulns": []map[string]string{{"id": "DEBIAN-CVE-2024-5535"}, {"id": "DSA-5800-1"}}})
				} else {
					results = append(results, map[string]any{"vulns": []map[string]string{{"id": "GHSA-p6mc-m468-83gw"}}})
				}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"results": results})
		case strings.HasPrefix(r.URL.Path, "/vulns/"):
			id := strings.TrimPrefix(r.URL.Path, "/vulns/")
			rec := map[string]any{"id": id, "summary": "about " + id}
			switch id {
			case "DSA-5800-1":
				rec["upstream"] = []string{"CVE-2024-5535"}
			case "GHSA-p6mc-m468-83gw":
				rec["aliases"] = []string{"CVE-2020-8203"}
				rec["database_specific"] = map[string]string{"severity": "HIGH"}
				rec["affected"] = []map[string]any{{
					"package": map[string]string{"name": "lodash", "ecosystem": "npm"},
					"ranges":  []map[string]any{{"events": []map[string]string{{"introduced": "3.7.0"}, {"fixed": "4.17.19"}}}},
				}}
			case "DEBIAN-CVE-2024-5535":
				rec["affected"] = []map[string]any{{
					"package": map[string]string{"name": "openssl", "ecosystem": "Debian:12"},
					"ranges":  []map[string]any{{"events": []map[string]string{{"introduced": "0"}, {"fixed": "3.0.15-1~deb12u1"}}}},
				}}
			}
			_ = json.NewEncoder(w).Encode(rec)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer osv.Close()

	l := newLab(t)
	ctx := context.Background()
	reg := &sbomTags{fakeTags: l.tags}
	l.checker = NewChecker(l.st, reg, slog.New(slog.NewTextHandler(io.Discard, nil)), time.Hour)
	l.checker.SetOSV(NewOSV(osv.URL))
	tgt := l.targets["prod-a"]
	if err := l.st.ApplySnapshot(ctx, store.SnapshotChanges{Scope: l.sc, TargetID: tgt.ID, SnapshotID: store.NewID(), At: time.Now(), Upsert: []store.Instance{
		{
			TargetID: tgt.ID, EnvironmentID: tgt.EnvironmentID, ServiceID: l.svc.ID, WorkloadID: "w", WorkloadName: "web", ContainerName: "nginx",
			Image: "nginx:1.25.3", Tag: "1.25.3", Digest: "sha256:abc", Running: 1, IsMain: true,
		},
	}}); err != nil {
		t.Fatal(err)
	}
	// No SBOM attestation known yet: nothing is read.
	if err := l.checker.CheckVulnerabilities(ctx, l.sc); err != nil || reg.reads.Load() != 0 {
		t.Fatalf("read without an SBOM attestation: %d %v", reg.reads.Load(), err)
	}
	_ = l.st.SetImageEvidence(ctx, l.sc, store.ImageEvidence{Repo: "docker.io/library/nginx", Digest: "sha256:abc", SBOM: true})
	if err := l.checker.CheckVulnerabilities(ctx, l.sc); err != nil {
		t.Fatal(err)
	}
	if reg.reads.Load() != 1 || batches.Load() != 1 {
		t.Fatalf("sbom reads %d, osv batches %d", reg.reads.Load(), batches.Load())
	}
	// Within the TTLs, neither the SBOM nor OSV is asked again.
	_ = l.checker.CheckVulnerabilities(ctx, l.sc)
	if reg.reads.Load() != 1 || batches.Load() != 1 {
		t.Fatalf("asked again: sbom %d, osv %d", reg.reads.Load(), batches.Load())
	}

	// CISA lists the OpenSSL one as exploited: it comes first, whatever its severity.
	if err := l.st.SetKnownExploited(ctx, []store.Exploited{{CVE: "CVE-2024-5535", Name: "OpenSSL", DueDate: "2026-11-01", Ransomware: true}}); err != nil {
		t.Fatal(err)
	}
	r, err := LoadVulnReport(ctx, l.st, l.sc, tgt.EnvironmentID)
	if err != nil {
		t.Fatal(err)
	}
	if r.Images != 1 || r.WithSBOM != 1 || r.Packages != 2 || len(r.Findings) != 2 || r.Pending != 0 || r.Unasked != 0 {
		t.Fatalf("report %+v", r)
	}
	// The Debian advisories of one CVE are one finding, exploited and so first, with the version that fixes it.
	if f := r.Findings[0]; f.Key != "CVE-2024-5535" || len(f.IDs) != 2 || f.Exploited == nil || !f.Exploited.Ransomware ||
		f.Affected[0].Service != l.svc.Name || f.Affected[0].Package != "openssl 3.0.11-1~deb12u2" || f.Affected[0].Fix != "3.0.15-1~deb12u1" {
		t.Errorf("first finding %+v", f)
	}
	// The GHSA is known by its CVE, with its severity and fix.
	if f := r.Findings[1]; f.Key != "CVE-2020-8203" || f.Severity != "HIGH" || f.Affected[0].Package != "lodash 4.17.15" || f.Affected[0].Fix != "4.17.19" {
		t.Errorf("second finding %+v", f)
	}
	if r.Exploited != 1 || r.Fixable != 2 {
		t.Errorf("exploited %d, fixable %d", r.Exploited, r.Fixable)
	}

	// An advisory whose record is not read yet (no CVE in its ID) leaves the answer incomplete.
	_ = l.st.SetImageSBOM(ctx, l.sc, store.ImageSBOM{Repo: "docker.io/library/nginx", Digest: "sha256:abc", Purls: []string{"pkg:npm/lodash@4.17.20"}})
	_ = l.st.SetPackageVulns(ctx, map[string][]string{"pkg:npm/lodash@4.17.20": {"GHSA-new-one"}})
	if r, _ := LoadVulnReport(ctx, l.st, l.sc, ""); r.Pending != 1 || len(r.Findings) != 1 || r.Findings[0].Key != "GHSA-new-one" {
		t.Fatalf("an advisory without its record is not pending: %+v", r)
	}
}

func TestKEV(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"catalogVersion":"2026.10.08","vulnerabilities":[
			{"cveID":"CVE-2024-3094","vendorProject":"XZ","product":"Utils","vulnerabilityName":"XZ backdoor","dateAdded":"2024-04-01","dueDate":"2024-04-22","knownRansomwareCampaignUse":"Unknown"},
			{"cveID":"CVE-2023-4966","vendorProject":"Citrix","product":"NetScaler","vulnerabilityName":"Citrix Bleed","dateAdded":"2023-10-18","dueDate":"2023-11-08","knownRansomwareCampaignUse":"Known"}]}`))
	}))
	defer srv.Close()
	l := newLab(t)
	ctx := context.Background()
	l.checker.SetKEV(NewKEV(srv.URL))
	if err := l.checker.refreshKEV(ctx); err != nil {
		t.Fatal(err)
	}
	got, fetched, err := l.st.KnownExploited(ctx)
	if err != nil || len(got) != 2 || fetched.IsZero() || !got["CVE-2023-4966"].Ransomware || got["CVE-2024-3094"].DueDate != "2024-04-22" {
		t.Fatalf("kev %+v %v", got, err)
	}
	// An empty download does not wipe the catalog.
	l.checker.SetKEV(NewKEV(httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"vulnerabilities":[]}`))
	})).URL))
	l.checker.now = func() time.Time { return time.Now().UTC().Add(25 * time.Hour) }
	_ = l.checker.refreshKEV(ctx)
	if got, _, _ := l.st.KnownExploited(ctx); len(got) != 2 {
		t.Fatalf("empty catalog replaced the known one: %d", len(got))
	}
}

func TestChooseFix(t *testing.T) {
	for _, c := range []struct {
		running string
		fixes   []string
		want    string
	}{
		{"2.14.1", []string{"2.12.2", "2.15.0", "2.3.1"}, "2.15.0"},
		{"4.17.15", []string{"4.17.19"}, "4.17.19"},
		{"3.0.11-1~deb12u2", []string{"3.0.15-1~deb12u1"}, "3.0.15-1~deb12u1"},
		{"weird", []string{"1.0", "2.0"}, "2.0"},
		{"1.0", nil, ""},
	} {
		if got := chooseFix(c.running, c.fixes); got != c.want {
			t.Errorf("%s %v: %q, want %q", c.running, c.fixes, got, c.want)
		}
	}
}

func TestAlertVulnerabilities(t *testing.T) {
	l := newLab(t)
	ctx := context.Background()
	l.checker.SetOSV(NewOSV("http://osv.invalid"))
	var announced []store.Event
	l.checker.OnEvents(func(_ store.Scope, evs []store.Event) { announced = append(announced, evs...) })
	tgt := l.targets["prod-a"]
	if err := l.st.ApplySnapshot(ctx, store.SnapshotChanges{Scope: l.sc, TargetID: tgt.ID, SnapshotID: store.NewID(), At: time.Now(), Upsert: []store.Instance{
		{
			TargetID: tgt.ID, EnvironmentID: tgt.EnvironmentID, ServiceID: l.svc.ID, WorkloadID: "w", WorkloadName: "pay", ContainerName: "app",
			Image: "ghcr.io/acme/pay:1", Tag: "1", Digest: "sha256:pay", Running: 1, IsMain: true,
		},
	}}); err != nil {
		t.Fatal(err)
	}
	log4j := "pkg:maven/org.apache.logging.log4j/log4j-core@2.14.1"
	_ = l.st.SetImageSBOM(ctx, l.sc, store.ImageSBOM{Repo: "ghcr.io/acme/pay", Digest: "sha256:pay", Purls: []string{log4j}})
	_ = l.st.SetPackageVulns(ctx, map[string][]string{log4j: {"GHSA-old"}})
	_ = l.st.SetVuln(ctx, store.Vuln{ID: "GHSA-old", Aliases: []string{"CVE-2020-1"}, Severity: "CRITICAL", Fixes: map[string][]string{}})

	// The first complete look is recorded, not announced.
	if err := l.checker.AlertVulnerabilities(ctx, l.sc); err != nil || len(announced) != 0 {
		t.Fatalf("first look announced %v %v", announced, err)
	}
	// A new exploited vulnerability is announced once, with its fix.
	_ = l.st.SetPackageVulns(ctx, map[string][]string{log4j: {"GHSA-old", "GHSA-jfh8-c2jp-5v3q"}})
	_ = l.st.SetVuln(ctx, store.Vuln{
		ID: "GHSA-jfh8-c2jp-5v3q", Aliases: []string{"CVE-2021-44228"}, Severity: "CRITICAL",
		Fixes: map[string][]string{"Maven|org.apache.logging.log4j:log4j-core": {"2.12.2", "2.15.0"}},
	})
	_ = l.st.SetKnownExploited(ctx, []store.Exploited{{CVE: "CVE-2021-44228", Ransomware: true}})
	if err := l.checker.AlertVulnerabilities(ctx, l.sc); err != nil {
		t.Fatal(err)
	}
	if len(announced) != 1 {
		t.Fatalf("announced %+v", announced)
	}
	e := announced[0]
	if e.Type != "vulnerability" || e.ServiceID != l.svc.ID || e.EnvironmentID != tgt.EnvironmentID || e.Note != "CVE-2021-44228 exploited ransomware critical" ||
		e.FromVersion != "log4j-core 2.14.1" || e.ToVersion != "2.15.0" {
		t.Fatalf("event %+v", e)
	}
	_ = l.checker.AlertVulnerabilities(ctx, l.sc)
	if len(announced) != 1 {
		t.Fatalf("announced again: %+v", announced)
	}
}
