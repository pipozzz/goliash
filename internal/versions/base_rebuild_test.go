// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package versions

import (
	"context"
	"testing"
	"time"

	"github.com/pipozzz/goliash/internal/store"
)

// An image built on an older build of its base: what the base's current build no longer has, a rebuild with --pull
// fixes; what it still has, or the image added itself, it does not.
func TestRebuildOnBaseFixes(t *testing.T) {
	l := newLab(t)
	ctx := context.Background()
	tgt := l.targets["prod-a"]
	if err := l.st.ApplySnapshot(ctx, store.SnapshotChanges{Scope: l.sc, TargetID: tgt.ID, SnapshotID: store.NewID(), At: time.Now(), Upsert: []store.Instance{{
		TargetID: tgt.ID, EnvironmentID: tgt.EnvironmentID, ServiceID: l.svc.ID, WorkloadID: "w", WorkloadName: "web", ContainerName: "web",
		Image: "example.com/web:1", Tag: "1", Digest: "sha256:img", Running: 1, IsMain: true,
	}}}); err != nil {
		t.Fatal(err)
	}
	const (
		oldSSL  = "pkg:apk/alpine/libssl3@3.3.1-r0?arch=x86_64&os_version=3.20.1&origin=openssl"
		oldZlib = "pkg:apk/alpine/zlib@1.3.1-r0?arch=x86_64&os_version=3.20.1"
		curl    = "pkg:apk/alpine/curl@8.9.0-r0?arch=x86_64&os_version=3.20.1"
		newSSL  = "pkg:apk/alpine/libssl3@3.3.2-r1?arch=x86_64&os_version=3.20.3&origin=openssl"
		newZlib = "pkg:apk/alpine/zlib@1.3.1-r1?arch=x86_64&os_version=3.20.3"
	)
	_ = l.st.SetImageSBOM(ctx, l.sc, store.ImageSBOM{Repo: "example.com/web", Digest: "sha256:img", Purls: []string{oldSSL, oldZlib, curl}, Source: "packages"})
	_ = l.st.SetImageSBOM(ctx, l.sc, store.ImageSBOM{Repo: "docker.io/library/alpine", Digest: "sha256:base", Purls: []string{newSSL, newZlib}, Source: "packages"})
	_ = l.st.SetImageEvidence(ctx, l.sc, store.ImageEvidence{
		Repo: "example.com/web", Digest: "sha256:img", BaseImage: "docker.io/library/alpine:3.20", BaseStatus: "outdated", BaseDigest: "sha256:base",
	})
	_ = l.st.SetPackageVulns(ctx, map[string][]string{
		oldSSL: {"ALPINE-CVE-2024-10001"}, oldZlib: {"ALPINE-CVE-2024-10002"}, curl: {"ALPINE-CVE-2024-10003"},
		newSSL: nil, newZlib: {"CVE-2024-10002"}, // zlib's under another ID naming the same CVE: still there
	})
	_ = l.st.SetVuln(ctx, store.Vuln{ID: "ALPINE-CVE-2024-10002", Aliases: []string{"CVE-2024-10002"}})
	r, err := LoadVulnReport(ctx, l.st, l.sc, "")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, f := range r.Findings {
		got[f.Key] = f.Affected[0].Rebuild
		if f.Key == "CVE-2024-10001" && !f.Fixable {
			t.Error("fixed by a rebuild, not fixable")
		}
	}
	if len(got) != 3 || got["CVE-2024-10001"] != "alpine:3.20" || got["CVE-2024-10002"] != "" || got["CVE-2024-10003"] != "" {
		t.Fatalf("rebuild fixes %v", got)
	}

	// A base that is current, or outdated further down its chain, is not rebuilt on.
	for _, ev := range []store.ImageEvidence{
		{BaseImage: "docker.io/library/alpine:3.20", BaseStatus: "current", BaseDigest: "sha256:base"},
		{
			Repo: "example.com/web", BaseImage: "ghcr.io/acme/base:1", BaseStatus: "outdated", BaseDigest: "sha256:b",
			BaseChain: []store.BaseLink{{Image: "example.com/web", Base: "ghcr.io/acme/base:1", Status: "current"}, {Image: "ghcr.io/acme/base:1", Base: "alpine:3.20", Status: "outdated"}},
		},
	} {
		if b, ok := RebuildBase(ev); ok {
			t.Errorf("%+v rebuilt on %s", ev, b)
		}
	}
}
