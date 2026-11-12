// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package versions

import (
	"fmt"
	"runtime"
	"testing"
	"time"

	"github.com/pipozzz/goliash/internal/store"
)

// reportBudget is what building the report of a Linux server with a kernel's thousands of vulnerabilities may
// allocate. Goliash runs on 1 GB hosts beside other services: 1.236.0 to 1.243.0 allocated ~420 MB here, the host
// swapped and every process read the disk again. Today it is ~45 MB.
const reportBudget = 120 << 20

func TestReportMemoryBudget(t *testing.T) {
	if testing.Short() || raceDetector {
		t.Skip("measures allocations, which the race detector multiplies (main's CI runs with it, pull requests without)")
	}
	l := newLab(t)
	ctx := t.Context()
	tgt := l.targets["prod-a"]
	if err := l.st.ApplySnapshot(ctx, store.SnapshotChanges{Scope: l.sc, TargetID: tgt.ID, SnapshotID: store.NewID(), At: time.Now(), Upsert: []store.Instance{{
		TargetID: tgt.ID, EnvironmentID: tgt.EnvironmentID, ServiceID: l.svc.ID, WorkloadID: "os/ubuntu", WorkloadKind: "host_os", WorkloadName: "ubuntu",
		ContainerName: "ubuntu", Image: "docker.io/library/ubuntu:24.04", Tag: "24.04", Running: 1, IsMain: true, CodeDigest: "sha256:h",
	}}}); err != nil {
		t.Fatal(err)
	}
	kernel := "pkg:deb/ubuntu/linux-image-6.17.0-1010-aws@6.17.0-1010.10~24.04.1?arch=amd64&os_version=24.04&upstream=linux-aws-6.17"
	modules := "pkg:deb/ubuntu/linux-modules-6.17.0-1010-aws@6.17.0-1010.10~24.04.1?arch=amd64&os_version=24.04&upstream=linux-aws-6.17"
	purls := []string{kernel, modules}
	for i := range 1500 {
		purls = append(purls, fmt.Sprintf("pkg:deb/ubuntu/pkg%d@1.0-1?arch=amd64&os_version=24.04", i))
	}
	_ = l.st.SetImageSBOM(ctx, l.sc, store.ImageSBOM{Repo: store.LambdaCodeRepo, Digest: "sha256:h", Source: "code", Purls: purls})
	ids := make([]string, 0, 8086)
	for i := range 8086 {
		ids = append(ids, fmt.Sprintf("UBUNTU-CVE-2025-%05d", i))
	}
	_ = l.st.SetPackageVulns(ctx, map[string][]string{kernel: ids, NewestKey(kernel): ids[247:], modules: ids, NewestKey(modules): ids[247:]})
	// The records kept: the exploited ones, each listing every kernel flavour's fix.
	fixes := map[string][]string{}
	for i := range 137 {
		fixes[fmt.Sprintf("Ubuntu:24.04:LTS|linux-flavour-%d", i)] = []string{"6.17.0-1011.11~24.04.1"}
	}
	for _, id := range ids[:40] {
		_ = l.st.SetVuln(ctx, store.Vuln{ID: id, Aliases: []string{id[7:]}, Severity: "HIGH", Fixes: fixes})
	}
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	r, err := loadVulnReport(ctx, l.st, l.sc, "")
	runtime.ReadMemStats(&after)
	if err != nil || len(r.Findings) != len(ids) {
		t.Fatalf("%d findings, %v", len(r.Findings), err)
	}
	if got := after.TotalAlloc - before.TotalAlloc; got > reportBudget {
		t.Errorf("the report of a server with a kernel allocated %d MB, over the %d MB budget", got>>20, reportBudget>>20)
	}
}
