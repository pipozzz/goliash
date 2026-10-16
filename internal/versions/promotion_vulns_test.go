// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package versions

import (
	"context"
	"testing"
	"time"

	"github.com/pipozzz/goliash/internal/store"
)

// Promoting 1.28.0 from staging to prod fixes the OpenSSL vulnerability 1.27.2 has and brings the curl one.
func TestAddVulnDeltas(t *testing.T) {
	l := newLab(t)
	ctx := context.Background()
	deploy := func(target, tag, digest string) {
		tg := l.targets[target]
		if err := l.st.ApplySnapshot(ctx, store.SnapshotChanges{Scope: l.sc, TargetID: tg.ID, SnapshotID: store.NewID(), At: time.Now(), Upsert: []store.Instance{{
			TargetID: tg.ID, EnvironmentID: tg.EnvironmentID, ServiceID: l.svc.ID, WorkloadID: "web", WorkloadName: "web", ContainerName: "nginx",
			Image: "nginx:" + tag, Tag: tag, Digest: digest, Running: 1, IsMain: true,
		}}}); err != nil {
			t.Fatal(err)
		}
	}
	deploy("stg-1", "1.28.0", "sha256:new")
	deploy("prod-a", "1.27.2", "sha256:old")
	openssl, curl, zlib := "pkg:deb/debian/openssl@3.0.11", "pkg:deb/debian/curl@7.88.1", "pkg:deb/debian/zlib@1.2.13"
	_ = l.st.SetImageSBOM(ctx, l.sc, store.ImageSBOM{Repo: "docker.io/library/nginx", Digest: "sha256:old", Purls: []string{openssl, zlib}})
	_ = l.st.SetImageSBOM(ctx, l.sc, store.ImageSBOM{Repo: "docker.io/library/nginx", Digest: "sha256:new", Purls: []string{curl, zlib}})
	_ = l.st.SetPackageVulns(ctx, map[string][]string{openssl: {"CVE-2024-5535"}, curl: {"CVE-2024-2398"}, zlib: {"CVE-2023-45853"}})
	_ = l.st.SetVuln(ctx, store.Vuln{ID: "CVE-2024-5535", Severity: "CRITICAL"})
	_ = l.st.SetVuln(ctx, store.Vuln{ID: "CVE-2024-2398", Severity: "MEDIUM"})
	_ = l.st.SetVuln(ctx, store.Vuln{ID: "CVE-2023-45853", Severity: "HIGH"})
	_ = l.st.SetKnownExploited(ctx, []store.Exploited{{CVE: "CVE-2024-5535", Name: "OpenSSL"}})

	list := []Promotion{
		{Service: l.svc, From: l.envs["staging"], To: l.envs["prod"], Version: "1.28.0", Running: "1.27.2"},
		{Service: l.svc, From: l.envs["dev"], To: l.envs["staging"], Version: "1.29.0", Running: "1.28.0"}, // no SBOM for 1.29.0
	}
	if err := AddVulnDeltas(ctx, l.st, l.sc, list); err != nil {
		t.Fatal(err)
	}
	d := list[0].Vulns
	if !d.Known || len(d.Fixed) != 1 || d.Fixed[0].ID != "CVE-2024-5535" || !d.Fixed[0].Exploited ||
		len(d.New) != 1 || d.New[0].ID != "CVE-2024-2398" || d.New[0].Severity != "MEDIUM" {
		t.Fatalf("staging → prod %+v", d)
	}
	if list[1].Vulns.Known {
		t.Errorf("dev → staging known without an SBOM: %+v", list[1].Vulns)
	}
	if Exploited(d.Fixed) != 1 || Exploited(d.New) != 0 {
		t.Error("exploited counts")
	}
}
