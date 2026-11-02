// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package versions

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pipozzz/goliash/internal/store"
)

// OSV still lists harfbuzz as affected, while Debian's tracker says bookworm never had the code; pam is not assessed.
func TestDebianTracker(t *testing.T) {
	var reads atomic.Int32
	tracker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reads.Add(1)
		_, _ = io.WriteString(w, `{
 "harfbuzz": {"CVE-2026-113679": {"description": "x", "releases": {
   "bookworm": {"status": "resolved", "repositories": {"bookworm": "6.0.0+dfsg-3"}, "fixed_version": "0", "urgency": "unimportant"},
   "sid": {"status": "resolved", "fixed_version": "0"}}},
   "TEMP-0000000-1": {"releases": {"bookworm": {"status": "undetermined"}}}},
 "pam": {"CVE-2025-8941": {"releases": {"bookworm": {"status": "undetermined", "urgency": "not yet assigned"}}}},
 "openssl": {"CVE-2024-5535": {"releases": {"bookworm": {"status": "resolved", "fixed_version": "3.0.15-1~deb12u1"}}}}
}`)
	}))
	defer tracker.Close()

	l := newLab(t)
	ctx := context.Background()
	l.checker = NewChecker(l.st, l.tags, slog.New(slog.NewTextHandler(io.Discard, nil)), time.Hour)
	l.checker.SetDebianTracker(tracker.URL)
	purls := []string{
		"pkg:deb/debian/libharfbuzz0b@6.0.0+dfsg-3?arch=amd64&os_version=12&upstream=harfbuzz",
		"pkg:deb/debian/libpam0g@1.5.2-6+deb12u1?arch=amd64&os_version=12&upstream=pam",
		"pkg:deb/debian/libssl3@3.0.11-1~deb12u2?arch=amd64&os_version=12&upstream=openssl",
	}
	if err := l.checker.refreshDebianTracker(ctx, purls); err != nil {
		t.Fatal(err)
	}
	statuses, fetched, err := l.st.DistroStatuses(ctx, "Debian")
	if err != nil || fetched.IsZero() || len(statuses) != 2 || statuses["Debian:12|harfbuzz|CVE-2026-113679"] != DistroNotAffected ||
		statuses["Debian:12|pam|CVE-2025-8941"] != DistroUndetermined {
		t.Fatalf("statuses %v %v", statuses, err)
	}
	if err := l.checker.refreshDebianTracker(ctx, purls); err != nil || reads.Load() != 1 {
		t.Fatalf("read again within a day: %d %v", reads.Load(), err)
	}
	if err := l.checker.refreshDebianTracker(ctx, []string{"pkg:npm/x@1"}); err != nil || reads.Load() != 1 {
		t.Fatal("read with no Debian package running")
	}

	tgt := l.targets["prod-a"]
	if err := l.st.ApplySnapshot(ctx, store.SnapshotChanges{Scope: l.sc, TargetID: tgt.ID, SnapshotID: store.NewID(), At: time.Now(), Upsert: []store.Instance{{
		TargetID: tgt.ID, EnvironmentID: tgt.EnvironmentID, ServiceID: l.svc.ID, WorkloadID: "w", WorkloadName: "web", ContainerName: "web",
		Image: "example.com/web:1", Tag: "1", Digest: "sha256:abc", Running: 1, IsMain: true,
	}}}); err != nil {
		t.Fatal(err)
	}
	_ = l.st.SetImageSBOM(ctx, l.sc, store.ImageSBOM{Repo: "example.com/web", Digest: "sha256:abc", Purls: purls, Source: "packages"})
	_ = l.st.SetPackageVulns(ctx, map[string][]string{
		purls[0]: {"DEBIAN-CVE-2026-113679"}, purls[1]: {"DEBIAN-CVE-2025-8941"}, purls[2]: {"DEBIAN-CVE-2024-5535"},
	})
	r, err := LoadVulnReport(ctx, l.st, l.sc, "")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, f := range r.Findings {
		got[f.Key] = f.Affected[0].Distro
	}
	if _, ok := got["CVE-2026-113679"]; ok || len(got) != 2 || got["CVE-2025-8941"] != DistroUndetermined || got["CVE-2024-5535"] != "" {
		t.Fatalf("findings %v", got)
	}
	if len(r.NotAffected) != 1 || !strings.Contains(r.NotAffected[0].Auto, "never had the vulnerable code (Debian security tracker)") {
		t.Fatalf("not affected %+v", r.NotAffected)
	}
}
