// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"context"
	"testing"
)

func TestVulnerabilityStore(t *testing.T) {
	forEachDialect(t, func(t *testing.T, s *Store) {
		ctx := context.Background()
		f := setup(t, s)
		sc := f.ws.Scope()
		if err := s.SetImageSBOM(ctx, sc, ImageSBOM{Repo: "docker.io/library/nginx", Digest: "sha256:a", Purls: []string{"pkg:deb/debian/openssl@3.0.11"}}); err != nil {
			t.Fatal(err)
		}
		sboms, err := s.ListImageSBOMs(ctx, sc)
		if b := sboms["docker.io/library/nginx@sha256:a"]; err != nil || len(b.Purls) != 1 || !b.LicensesRead || len(b.Licenses) != 0 {
			t.Fatalf("sboms %+v %v", sboms, err)
		}
		lic := map[string]string{"pkg:deb/debian/openssl@3.0.11": "Apache-2.0"}
		_ = s.SetImageSBOM(ctx, sc, ImageSBOM{Repo: "docker.io/library/nginx", Digest: "sha256:a", Purls: []string{"pkg:deb/debian/openssl@3.0.11"}, Licenses: lic})
		if sboms, _ = s.ListImageSBOMs(ctx, sc); sboms["docker.io/library/nginx@sha256:a"].Licenses["pkg:deb/debian/openssl@3.0.11"] != "Apache-2.0" {
			t.Fatalf("licenses %+v", sboms)
		}
		// Read before licenses were kept.
		if _, err := s.exec(ctx, s.db, `UPDATE image_sboms SET licenses = NULL`); err != nil {
			t.Fatal(err)
		}
		if sboms, _ = s.ListImageSBOMs(ctx, sc); sboms["docker.io/library/nginx@sha256:a"].LicensesRead {
			t.Fatal("an SBOM read before licenses counts as read for them")
		}
		if err := s.SetPackageVulns(ctx, map[string][]string{"pkg:deb/debian/openssl@3.0.11": {"DEBIAN-CVE-2024-1", "DSA-1"}, "pkg:npm/x@1": nil}); err != nil {
			t.Fatal(err)
		}
		pv, err := s.PackageVulnsOf(ctx, []string{"pkg:deb/debian/openssl@3.0.11", "pkg:npm/x@1", "pkg:npm/unknown@1"})
		if err != nil || len(pv) != 2 || len(pv["pkg:deb/debian/openssl@3.0.11"].Vulns) != 2 || len(pv["pkg:npm/x@1"].Vulns) != 0 {
			t.Fatalf("package vulns %+v %v", pv, err)
		}
		if err := s.SetVuln(ctx, Vuln{ID: "DSA-1", Aliases: []string{"CVE-2024-1"}, Summary: "openssl", Severity: "HIGH"}); err != nil {
			t.Fatal(err)
		}
		vs, err := s.ListVulns(ctx)
		if err != nil || vs["DSA-1"].Severity != "HIGH" || vs["DSA-1"].Aliases[0] != "CVE-2024-1" {
			t.Fatalf("vulns %+v %v", vs, err)
		}
	})
}
