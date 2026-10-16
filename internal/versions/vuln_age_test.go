// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package versions

import (
	"context"
	"testing"
	"time"

	"github.com/pipozzz/goliash/internal/store"
)

// A vulnerability runs since its image started running, and is past due once CISA's date for fixing it passed.
func TestVulnerabilityAge(t *testing.T) {
	l := newLab(t)
	ctx := context.Background()
	tg := l.targets["prod-a"]
	started := time.Now().Add(-10 * 24 * time.Hour)
	if err := l.st.ApplySnapshot(ctx, store.SnapshotChanges{Scope: l.sc, TargetID: tg.ID, SnapshotID: store.NewID(), At: started, Upsert: []store.Instance{{
		TargetID: tg.ID, EnvironmentID: tg.EnvironmentID, ServiceID: l.svc.ID, WorkloadID: "web", WorkloadName: "web", ContainerName: "nginx",
		Image: "nginx:1.27.2", Tag: "1.27.2", Digest: "sha256:old", Running: 1, IsMain: true,
	}}}); err != nil {
		t.Fatal(err)
	}
	openssl, zlib := "pkg:deb/debian/openssl@3.0.11", "pkg:deb/debian/zlib@1.2.13"
	_ = l.st.SetImageSBOM(ctx, l.sc, store.ImageSBOM{Repo: "docker.io/library/nginx", Digest: "sha256:old", Purls: []string{openssl, zlib}})
	_ = l.st.SetPackageVulns(ctx, map[string][]string{openssl: {"CVE-2024-5535"}, zlib: {"CVE-2023-45853"}})
	_ = l.st.SetVuln(ctx, store.Vuln{ID: "CVE-2024-5535", Severity: "CRITICAL"})
	_ = l.st.SetVuln(ctx, store.Vuln{ID: "CVE-2023-45853", Severity: "HIGH"})
	_ = l.st.SetKnownExploited(ctx, []store.Exploited{{CVE: "CVE-2024-5535", Name: "OpenSSL", DueDate: "2024-07-01"}})

	r, err := LoadVulnReport(ctx, l.st, l.sc, l.envs["prod"].ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Findings) != 2 || r.PastDue != 1 {
		t.Fatalf("report %+v", r)
	}
	f := r.Findings[0]
	now := time.Now()
	if f.Key != "CVE-2024-5535" || !f.PastDue(now) || f.DaysRunning(now) != 10 || f.Affected[0].Since.IsZero() {
		t.Fatalf("finding %+v, %d days", f, f.DaysRunning(now))
	}
	if r.Findings[1].PastDue(now) {
		t.Error("not exploited, so never past due")
	}
	if (VulnFinding{Exploited: &store.Exploited{DueDate: now.AddDate(0, 0, 3).Format("2006-01-02")}}).PastDue(now) {
		t.Error("due in three days is not past due")
	}
}
