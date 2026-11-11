// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package versions

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/pipozzz/goliash/internal/store"
)

// A Linux kernel has thousands of vulnerabilities; OSV answers a thousand a query and a page token for the rest.
func TestQueryBatchFollowsPages(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Queries []osvQuery `json:"queries"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		var results []map[string]any
		for _, q := range req.Queries {
			if q.Package.Name != "linux-aws-6.17" {
				results = append(results, map[string]any{"vulns": []map[string]string{{"id": "UBUNTU-CVE-2026-1"}}})
				continue
			}
			page := map[string]int{"": 0, "p1": 1, "p2": 2}[q.PageToken]
			var vulns []map[string]string
			for i := range 3 {
				vulns = append(vulns, map[string]string{"id": fmt.Sprintf("UBUNTU-CVE-2026-%d%d", page, i)})
			}
			res := map[string]any{"vulns": vulns}
			if page < 2 {
				res["next_page_token"] = fmt.Sprintf("p%d", page+1)
			}
			results = append(results, res)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"results": results})
	}))
	defer srv.Close()
	o := &OSV{BaseURL: srv.URL, HTTP: srv.Client()}
	kernel := "pkg:deb/ubuntu/linux-modules-6.17.0-1010-aws@6.17.0-1010.10~24.04.1?os_version=24.04&upstream=linux-aws-6.17"
	other := "pkg:deb/ubuntu/curl@8.5.0-2ubuntu10.6?os_version=24.04"
	got, err := o.QueryBatch(t.Context(), []string{kernel, other})
	if err != nil {
		t.Fatal(err)
	}
	if len(got[kernel]) != 9 || len(got[other]) != 1 {
		t.Fatalf("kernel %d, curl %d: %v", len(got[kernel]), len(got[other]), got)
	}
}

// The vulnerabilities a package's newest version no longer has are fixed by a newer version: asked as IDs, since a
// Linux kernel's records weigh gigabytes.
func TestQueryNewest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Queries []osvQuery `json:"queries"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		var results []map[string]any
		for _, q := range req.Queries {
			if q.Version != newestVersion {
				t.Errorf("asked %+v", q)
			}
			results = append(results, map[string]any{"vulns": []map[string]string{{"id": "UBUNTU-CVE-2026-2"}}})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"results": results})
	}))
	defer srv.Close()
	o := &OSV{BaseURL: srv.URL, HTTP: srv.Client()}
	kernel := "pkg:deb/ubuntu/linux-modules-6.17.0-1010-aws@6.17.0-1010.10~24.04.1?os_version=24.04&upstream=linux-aws-6.17"
	got, err := o.QueryNewest(t.Context(), []string{kernel, "pkg:npm/lodash@4.17.15"})
	if err != nil || len(got) != 1 || len(got[kernel]) != 1 {
		t.Fatalf("%v %v", got, err)
	}
}

func TestFixedInNewerVersion(t *testing.T) {
	l := newLab(t)
	ctx := t.Context()
	kernel := "pkg:deb/ubuntu/libcurl4t64@8.5.0-2ubuntu10.6?arch=amd64&os_version=24.04&upstream=curl"
	tgt := l.targets["prod-a"]
	if err := l.st.ApplySnapshot(ctx, store.SnapshotChanges{Scope: l.sc, TargetID: tgt.ID, SnapshotID: store.NewID(), At: time.Now(), Upsert: []store.Instance{{
		TargetID: tgt.ID, EnvironmentID: tgt.EnvironmentID, ServiceID: l.svc.ID, WorkloadID: "w", WorkloadName: "web", ContainerName: "web",
		Image: "example.com/web:1", Tag: "1", Digest: "sha256:abc", Running: 1, IsMain: true,
	}}}); err != nil {
		t.Fatal(err)
	}
	_ = l.st.SetImageSBOM(ctx, l.sc, store.ImageSBOM{Repo: "example.com/web", Digest: "sha256:abc", Purls: []string{kernel}, Source: "packages"})
	// Two advisories, no records read; the newest curl still has one.
	_ = l.st.SetPackageVulns(ctx, map[string][]string{
		kernel: {"UBUNTU-CVE-2026-10001", "UBUNTU-CVE-2026-10002"}, NewestKey(kernel): {"UBUNTU-CVE-2026-10002"},
	})
	r, err := LoadVulnReport(ctx, l.st, l.sc, "")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, f := range r.Findings {
		got[f.Key] = f.Fixable
		if f.Key == "CVE-2026-10001" && !f.Affected[0].FixNewer {
			t.Error("not fixed in a newer version")
		}
	}
	if len(got) != 2 || !got["CVE-2026-10001"] || got["CVE-2026-10002"] {
		t.Fatalf("fixable %v", got)
	}
}

// telnet 0.17+2.5-3ubuntu4.2 is built from inetutils 2:2.5-3ubuntu4.2: OSV is asked about the source's version, not
// the binary's, which compares as older than every inetutils fix.
func TestSourceVersionAsked(t *testing.T) {
	p := "pkg:deb/ubuntu/telnet@0.17%2B2.5-3ubuntu4.2?os_version=24.04&upstream=inetutils%402%3A2.5-3ubuntu4.2"
	qs := osvQueries(p)
	if len(qs) == 0 || qs[0].Package.Name != "inetutils" || qs[0].Version != "2:2.5-3ubuntu4.2" {
		t.Fatalf("%+v", qs)
	}
	if v := advisoryVersion(p); v != "2:2.5-3ubuntu4.2" {
		t.Errorf("advisory version %q", v)
	}
	// Without the source's version (an older agent, or the same version), the binary's is asked.
	if qs := osvQueries("pkg:deb/ubuntu/libssl3@3.0.13-0ubuntu3.5?os_version=24.04&upstream=openssl"); qs[0].Version != "3.0.13-0ubuntu3.5" || qs[0].Package.Name != "openssl" {
		t.Errorf("%+v", qs[0])
	}
}

func TestKernelBuildPackage(t *testing.T) {
	for pkg, want := range map[string]bool{
		"linux-aws-6.17-headers-6.17.0-1009 6.17.0-1009.9~24.04.2": true,
		"linux-aws-6.17-tools-6.17.0-1010 6.17.0-1010.10~24.04.1":  true,
		"linux-tools-6.17.0-1010-aws 6.17.0-1010.10~24.04.1":       true,
		"linux-tools-common 6.8.0-149.149":                         true,
		"linux-headers-6.8.0-1015-aws 6.8.0-1015.16":               true,
		"linux-libc-dev 6.8.0-149.149":                             true,
		"linux-modules-6.17.0-1010-aws 6.17.0-1010.10~24.04.1":     false,
		"linux-image-6.17.0-1010-aws 6.17.0-1010.10~24.04.1":       false,
		"telnet 0.17+2.5-3ubuntu4.2":                               false,
	} {
		if got := kernelBuildPackage(pkg); got != want {
			t.Errorf("%s: %v", pkg, got)
		}
	}
	a := Affected{Package: "linux-aws-6.17-headers-6.17.0-1009 6.17.0-1009.9~24.04.2", Host: true}
	if autoNotAffected(a) == "" {
		t.Error("kernel headers on a server counted")
	}
	a.Package = "linux-modules-6.17.0-1010-aws 6.17.0-1010.10~24.04.1"
	if autoNotAffected(a) != "" {
		t.Error("the running kernel's modules left out")
	}
}
