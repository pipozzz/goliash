// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package versions

import (
	"context"
	"testing"
	"time"

	"github.com/pipozzz/goliash/internal/store"
)

// A deploy replaces nginx:1 with nginx:2: what nginx:1 had is fixed (its package at a version without it), removed
// (with its package), still there, or, while nginx:2's answer is missing, not verified.
func TestResolutions(t *testing.T) {
	l := newLab(t)
	ctx := context.Background()
	tgt := l.targets["prod-a"]
	run := func(tag, digest string, existing []store.Instance) {
		ch := store.SnapshotChanges{Scope: l.sc, TargetID: tgt.ID, SnapshotID: store.NewID(), At: time.Now(), Upsert: []store.Instance{{
			TargetID: tgt.ID, EnvironmentID: tgt.EnvironmentID, ServiceID: l.svc.ID, WorkloadID: "w", WorkloadName: "web", ContainerName: "web",
			Image: "example.com/web:" + tag, Tag: tag, Digest: digest, Running: 1, IsMain: true,
		}}}
		for _, in := range existing {
			if in.Active() {
				ch.Remove = append(ch.Remove, in.ID)
			}
		}
		if err := l.st.ApplySnapshot(ctx, ch); err != nil {
			t.Fatal(err)
		}
	}
	const (
		brotliOld = "pkg:deb/debian/libbrotli1@1.0.9-2+b6?os_version=12&upstream=brotli"
		brotliNew = "pkg:deb/debian/libbrotli1@1.0.9-2+deb12u1?os_version=12&upstream=brotli"
		curl      = "pkg:deb/debian/curl@7.88.1-10?os_version=12"
		zlib      = "pkg:deb/debian/zlib1g@1.2.13?os_version=12"
		tar       = "pkg:deb/debian/tar@1.34?os_version=12"
	)
	_ = l.st.SetImageSBOM(ctx, l.sc, store.ImageSBOM{Repo: "example.com/web", Digest: "sha256:a", Purls: []string{brotliOld, curl, zlib}, Source: "packages"})
	_ = l.st.SetImageSBOM(ctx, l.sc, store.ImageSBOM{Repo: "example.com/web", Digest: "sha256:b", Purls: []string{brotliNew, zlib, tar}, Source: "packages"})
	_ = l.st.SetPackageVulns(ctx, map[string][]string{
		brotliOld: {"DEBIAN-CVE-2026-111497"}, curl: {"DEBIAN-CVE-2026-20001"}, zlib: {"DEBIAN-CVE-2026-30001"},
		brotliNew: nil, tar: nil,
	})
	_ = l.st.SetKnownExploited(ctx, []store.Exploited{{CVE: "CVE-2026-111497", DateAdded: "2026-10-27"}})
	run("1", "sha256:a", nil)
	existing, _ := l.st.ListTargetInstances(ctx, l.sc, tgt.ID)
	run("2", "sha256:b", existing)

	rs, err := Resolutions(ctx, l.st, l.sc, time.Now().Add(-time.Hour))
	if err != nil || len(rs) != 1 {
		t.Fatalf("%+v %v", rs, err)
	}
	r := rs[0]
	if r.From != "example.com/web:1" || r.To != "example.com/web:2" || !r.Verified || r.Still != 1 ||
		len(r.Fixed) != 1 || r.Fixed[0].CVE != "CVE-2026-111497" || !r.Fixed[0].Exploited || r.Fixed[0].To != "1.0.9-2+deb12u1" ||
		len(r.Removed) != 1 || r.Removed[0].Package != "curl" || len(r.Unverified) != 0 {
		t.Fatalf("resolution %+v", r)
	}

	// Before OSV answers for the new image's version, gone is not verified.
	_ = l.st.SetImageSBOM(ctx, l.sc, store.ImageSBOM{Repo: "example.com/web", Digest: "sha256:b", Purls: []string{"pkg:deb/debian/libbrotli1@1.0.9-2+deb12u2?os_version=12", zlib}, Source: "packages"})
	rs, _ = Resolutions(ctx, l.st, l.sc, time.Now().Add(-time.Hour))
	if len(rs) != 1 || len(rs[0].Fixed) != 0 || len(rs[0].Unverified) != 1 || rs[0].Unverified[0].CVE != "CVE-2026-111497" {
		t.Fatalf("unanswered %+v", rs)
	}
}
