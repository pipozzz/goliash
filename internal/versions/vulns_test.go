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

	r, err := LoadVulnReport(ctx, l.st, l.sc, tgt.EnvironmentID)
	if err != nil {
		t.Fatal(err)
	}
	if r.Images != 1 || r.WithSBOM != 1 || r.Packages != 2 || len(r.Findings) != 2 || r.Pending != 0 || r.Unasked != 0 {
		t.Fatalf("report %+v", r)
	}
	// The Debian advisories of one CVE are one finding; the GHSA is known by its CVE and ranks first (HIGH).
	if f := r.Findings[0]; f.Key != "CVE-2020-8203" || f.Severity != "HIGH" || f.Affected[0].Package != "lodash 4.17.15" {
		t.Errorf("first finding %+v", f)
	}
	if f := r.Findings[1]; f.Key != "CVE-2024-5535" || len(f.IDs) != 2 || f.Affected[0].Service != l.svc.Name || f.Affected[0].Package != "openssl 3.0.11-1~deb12u2" {
		t.Errorf("second finding %+v", f)
	}

	// An advisory whose record is not read yet (no CVE in its ID) leaves the answer incomplete.
	_ = l.st.SetImageSBOM(ctx, l.sc, store.ImageSBOM{Repo: "docker.io/library/nginx", Digest: "sha256:abc", Purls: []string{"pkg:npm/lodash@4.17.20"}})
	_ = l.st.SetPackageVulns(ctx, map[string][]string{"pkg:npm/lodash@4.17.20": {"GHSA-new-one"}})
	if r, _ := LoadVulnReport(ctx, l.st, l.sc, ""); r.Pending != 1 || len(r.Findings) != 1 || r.Findings[0].Key != "GHSA-new-one" {
		t.Fatalf("an advisory without its record is not pending: %+v", r)
	}
}
