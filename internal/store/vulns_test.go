// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"context"
	"os"
	"strings"
	"testing"
)

func mustSBOMs(t *testing.T, s *Store, sc Scope) map[string]ImageSBOM {
	t.Helper()
	m, err := s.ListImageSBOMs(context.Background(), sc)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

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
		// Read from the image: migration 00087 makes it be read again; a misread Go version is left out.
		_ = s.SetImageSBOM(ctx, sc, ImageSBOM{
			Repo: "docker.io/library/kuma", Digest: "sha256:k", Source: "packages",
			Purls: []string{"pkg:golang/stdlib@rServerServiceEngine", "pkg:golang/stdlib@1.20.5"},
		})
		up, err := os.ReadFile("migrations/" + string(s.dialect) + "/00087_reread_image_packages.sql") //nolint:gosec // the dialect, a constant
		if err != nil {
			t.Fatal(err)
		}
		stmt, _, _ := strings.Cut(string(up[strings.Index(string(up), "UPDATE"):]), ";")
		if _, err := s.exec(ctx, s.db, stmt); err != nil {
			t.Fatal(err)
		}
		k := mustSBOMs(t, s, sc)["docker.io/library/kuma@sha256:k"]
		if k.FetchedAt.Year() != 2000 || len(k.Purls) != 1 || k.Purls[0] != "pkg:golang/stdlib@1.20.5" || mustSBOMs(t, s, sc)["docker.io/library/nginx@sha256:a"].FetchedAt.Year() == 2000 {
			t.Fatalf("after 00087: %+v", k)
		}
		// 00089 reads again only the images with a Go executable.
		for _, sb := range []ImageSBOM{
			{Repo: "docker.io/library/traefik", Digest: "sha256:t", Source: "packages", Purls: []string{"pkg:golang/stdlib@1.22.5"}},
			{Repo: "docker.io/library/redis", Digest: "sha256:r", Source: "packages", Purls: []string{"pkg:deb/debian/redis@7"}},
		} {
			if err := s.SetImageSBOM(ctx, sc, sb); err != nil {
				t.Fatal(err)
			}
		}
		up, err = os.ReadFile("migrations/" + string(s.dialect) + "/00089_reread_go_programs.sql") //nolint:gosec // the dialect, a constant
		if err != nil {
			t.Fatal(err)
		}
		stmt, _, _ = strings.Cut(string(up[strings.Index(string(up), "UPDATE"):]), ";")
		if _, err := s.exec(ctx, s.db, stmt); err != nil {
			t.Fatal(err)
		}
		if sb := mustSBOMs(t, s, sc); sb["docker.io/library/traefik@sha256:t"].FetchedAt.Year() != 2000 || sb["docker.io/library/redis@sha256:r"].FetchedAt.Year() == 2000 {
			t.Fatalf("after 00089: %+v", sb)
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
