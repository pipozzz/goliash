// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package versions

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/pipozzz/goliash/internal/store"
)

func TestClassifyLicense(t *testing.T) {
	for expr, want := range map[string]LicenseClass{
		"MIT":                                 Permissive,
		"Apache-2.0":                          Permissive,
		"BSD-3-Clause OR ISC OR BSD-2-Clause": Permissive,
		"AGPL-3.0-only":                       NetworkCopyleft,
		"SSPL-1.0":                            NetworkCopyleft,
		"BUSL-1.1":                            SourceAvailable,
		"Elastic-2.0":                         SourceAvailable,
		"GPL-2.0-or-later":                    StrongCopyleft,
		"GPL-2+":                              StrongCopyleft,
		"LGPL-2.1-only":                       WeakCopyleft,
		"MPL-2.0":                             WeakCopyleft,
		"GPL-2.0-only WITH Classpath-exception-2.0":                              WeakCopyleft,
		"MIT OR GPL-2.0-only":                                                    Permissive,
		"MIT AND GPL-2.0-only":                                                   StrongCopyleft,
		"(MIT OR Apache-2.0) AND AGPL-3.0-or-later":                              NetworkCopyleft,
		"GPL-3.0-only AND (LGPL-2.1-only OR MIT)":                                StrongCopyleft,
		"LGPL-2.1-only OR GPL-2.0-only OR GPL-2.0-or-later OR LGPL-2.0-or-later": WeakCopyleft,
		"BSL-1.0":                Permissive,
		"CC-BY-SA-4.0":           StrongCopyleft,
		"CC-BY-4.0":              Permissive,
		"CC-BY-NC-4.0":           SourceAvailable,
		"":                       UnknownLicense,
		"LicenseRef-Proprietary": UnknownLicense,
		"MIT AND Custom":         UnknownLicense,
		"Custom OR MIT":          Permissive,
	} {
		if got := ClassifyLicense(expr); got != want {
			t.Errorf("ClassifyLicense(%q) = %s, want %s", expr, got, want)
		}
	}
}

func TestLicenseReport(t *testing.T) {
	l := newLab(t)
	ctx := context.Background()
	tgt, _ := l.st.CreateTarget(ctx, store.Target{Scope: l.sc, EnvironmentID: l.envs["prod"].ID, Platform: "kubernetes", Name: "k8s"})
	if err := l.st.ApplySnapshot(ctx, store.SnapshotChanges{Scope: l.sc, TargetID: tgt.ID, SnapshotID: store.NewID(), At: time.Now(), Upsert: []store.Instance{{
		TargetID: tgt.ID, EnvironmentID: tgt.EnvironmentID, ServiceID: l.svc.ID, WorkloadID: "w", WorkloadName: "web", ContainerName: "app",
		Image: "ghcr.io/acme/web:1.0.0", Tag: "1.0.0", Digest: "sha256:web", Running: 1, IsMain: true,
	}}}); err != nil {
		t.Fatal(err)
	}
	pdf, glibc, jwt, lodash := "pkg:maven/com.itextpdf/itextpdf@5.5.13.3", "pkg:deb/debian/libc6@2.36?os_distro=bookworm", "pkg:npm/jsonwebtoken@8.5.1", "pkg:npm/lodash@4.17.21"
	_ = l.st.SetImageSBOM(ctx, l.sc, store.ImageSBOM{
		Repo: "ghcr.io/acme/web", Digest: "sha256:web", Purls: []string{glibc, jwt, lodash, pdf},
		Licenses: map[string]string{pdf: "AGPL-3.0-only", glibc: "LGPL-2.1-or-later", lodash: "MIT"},
	})
	r, err := LoadLicenseReport(ctx, l.st, l.sc, "")
	if err != nil {
		t.Fatal(err)
	}
	if r.Images != 1 || r.WithSBOM != 1 || len(r.Packages) != 4 || r.LicensesUnread != 0 {
		t.Fatalf("report %+v", r)
	}
	first := r.Packages[0]
	if first.Purl != pdf || first.Class != NetworkCopyleft || first.OS || len(first.Where) != 1 || first.Where[0].Environment != "prod" {
		t.Fatalf("first %+v", first)
	}
	if r.Counts[NetworkCopyleft] != 1 || r.Counts[UnknownLicense] != 1 || r.Counts[Permissive] != 1 || r.OSCounts[WeakCopyleft] != 1 {
		t.Fatalf("counts %v os %v", r.Counts, r.OSCounts)
	}
	if !LicenseMatches(first, "agpl") || !LicenseMatches(first, "itextpdf") || LicenseMatches(first, "lodash") {
		t.Error("search")
	}
}

// The CSV exports keep spreadsheet formulas out: package names and summaries come from anyone's SBOMs.
func TestCSVExports(t *testing.T) {
	var b strings.Builder
	pkgs := []LicensedPackage{{
		Purl: "pkg:npm/%3Devil@1", Name: "=HYPERLINK(\"x\")", Version: "1", License: "MIT", Class: Permissive,
		Where: []LicenseUse{{Service: "web", Environment: "prod"}, {Service: "web", Environment: "dev"}},
	}}
	if err := WriteLicensesCSV(&b, pkgs); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(b.String()), "\n")
	if len(lines) != 3 || !strings.HasPrefix(lines[1], "permissive,\"'=HYPERLINK") || !strings.Contains(lines[1], ",web,prod,") {
		t.Errorf("licenses csv:\n%s", b.String())
	}
	b.Reset()
	f := VulnFinding{
		Key: "CVE-2021-44228", Severity: "CRITICAL", Summary: "-1+1", Exploited: &store.Exploited{DueDate: "2021-12-24"},
		Affected: []Affected{{Service: "pay", Env: "prod", Image: "ghcr.io/acme/pay:1", Package: "log4j-core 2.14.1", Fix: "2.15.0"}},
	}
	if err := WriteVulnsCSV(&b, []VulnFinding{f}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.String(), "CVE-2021-44228,,critical,true,false,2021-12-24,true,pay,prod,ghcr.io/acme/pay:1,log4j-core 2.14.1,2.15.0,,'-1+1") {
		t.Errorf("vulns csv:\n%s", b.String())
	}
}

// An upgrade's license changes: a package it brings under network copyleft, one relicensed to source-available;
// operating system packages, permissive and as-demanding-as-before ones are left out.
func TestUpgradeLicenses(t *testing.T) {
	l := newLab(t)
	ctx := context.Background()
	prod, _ := l.st.CreateTarget(ctx, store.Target{Scope: l.sc, EnvironmentID: l.envs["prod"].ID, Platform: "kubernetes", Name: "k8s"})
	stg, _ := l.st.CreateTarget(ctx, store.Target{Scope: l.sc, EnvironmentID: l.envs["staging"].ID, Platform: "kubernetes", Name: "k8s-stg"})
	run := func(tgt store.Target, tag string) {
		if err := l.st.ApplySnapshot(ctx, store.SnapshotChanges{Scope: l.sc, TargetID: tgt.ID, SnapshotID: store.NewID(), At: time.Now(), Upsert: []store.Instance{{
			TargetID: tgt.ID, EnvironmentID: tgt.EnvironmentID, ServiceID: l.svc.ID, WorkloadID: "w", WorkloadName: "web", ContainerName: "app",
			Image: "ghcr.io/acme/web:" + tag, Tag: tag, Digest: "sha256:" + tag, Running: 1, IsMain: true,
		}}}); err != nil {
			t.Fatal(err)
		}
	}
	run(prod, "1.0.0")
	run(stg, "2.0.0")
	old := map[string]string{"pkg:golang/github.com/hashicorp/vault@1.14.0": "MPL-2.0", "pkg:npm/gpl-thing@1.0.0": "GPL-3.0-only", "pkg:deb/debian/bash@5.2": "GPL-3.0"}
	newer := map[string]string{
		"pkg:golang/github.com/hashicorp/vault@1.15.0": "BUSL-1.1", "pkg:npm/gpl-thing@1.1.0": "GPL-3.0-only",
		"pkg:deb/debian/bash@5.2": "GPL-3.0", "pkg:maven/com.itextpdf/itextpdf@5.5.13": "AGPL-3.0-only", "pkg:npm/lodash@4.17.21": "MIT",
	}
	for tag, lic := range map[string]map[string]string{"1.0.0": old, "2.0.0": newer} {
		var purls []string
		for p := range lic {
			purls = append(purls, p)
		}
		if err := l.st.SetImageSBOM(ctx, l.sc, store.ImageSBOM{Repo: "ghcr.io/acme/web", Digest: "sha256:" + tag, Purls: purls, Licenses: lic}); err != nil {
			t.Fatal(err)
		}
	}
	d, err := UpgradeLicenses(ctx, l.st, l.sc, l.svc.ID, l.envs["prod"].ID, "1.0.0", "2.0.0")
	if err != nil || !d.Known || len(d.Changes) != 2 {
		t.Fatalf("delta %+v %v", d, err)
	}
	if c := d.Changes[0]; c.Name != "com.itextpdf/itextpdf" || c.Class != NetworkCopyleft || c.Was != "" {
		t.Errorf("first %+v", c)
	}
	if c := d.Changes[1]; c.Name != "github.com/hashicorp/vault" || c.Class != SourceAvailable || c.Was != "MPL-2.0" || c.Version != "1.15.0" {
		t.Errorf("second %+v", c)
	}
	if d, _ := UpgradeLicenses(ctx, l.st, l.sc, l.svc.ID, l.envs["prod"].ID, "1.0.0", "9.9.9"); d.Known {
		t.Error("known without the newer image's SBOM")
	}
}
