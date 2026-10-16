// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package versions

import (
	"context"
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
