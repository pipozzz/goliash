// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package versions

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/pipozzz/goliash/internal/store"
)

// An upgrade to a version that runs nowhere yet says what it fixes, from the packages of its release image; until
// OSV answered for every package, it says nothing.
func TestReleaseDeltas(t *testing.T) {
	l := newLab(t)
	ctx := context.Background()
	repo := "240220093055.dkr.ecr.us-west-2.amazonaws.com/proxysql"
	running := "sha256:1111111111111111111111111111111111111111111111111111111111111111"
	release := "sha256:2222222222222222222222222222222222222222222222222222222222222222"
	prod := l.envs["prod"]
	tg := l.targets["prod-a"]
	snap := store.NewID()
	_, _ = l.st.InsertSnapshot(ctx, store.Snapshot{ID: snap, Scope: l.sc, TargetID: tg.ID, CollectedAt: time.Now(), Complete: true, Payload: json.RawMessage(`{}`)})
	if err := l.st.ApplySnapshot(ctx, store.SnapshotChanges{Scope: l.sc, SnapshotID: snap, TargetID: tg.ID, At: time.Now(), Upsert: []store.Instance{{
		TargetID: tg.ID, EnvironmentID: prod.ID, ServiceID: l.private.ID, WorkloadID: "px", WorkloadKind: "service", WorkloadName: "proxysql",
		ContainerName: "proxysql", Image: repo + ":v2.7.2", Tag: "v2.7.2", Digest: running, Running: 1, IsMain: true,
	}}}); err != nil {
		t.Fatal(err)
	}
	brotliOld, brotliNew, git := "pkg:deb/debian/brotli@1.0.9-2", "pkg:deb/debian/brotli@1.0.9-2+deb12u1", "pkg:deb/debian/git@1:2.39.2-1.1"
	_ = l.st.SetImageSBOM(ctx, l.sc, store.ImageSBOM{Repo: repo, Digest: running, Purls: []string{brotliOld, git}, Source: "packages"})
	_ = l.st.SetImageSBOM(ctx, l.sc, store.ImageSBOM{Repo: repo, Digest: release, Purls: []string{brotliNew, git}, Source: "packages"})
	_ = l.st.SetReleaseImage(ctx, l.sc, store.ReleaseImage{Repo: repo, Tag: "v3.0.11", Digest: release})
	_ = l.st.SetKnownExploited(ctx, []store.Exploited{{CVE: "CVE-2026-111497"}, {CVE: "CVE-2025-48384"}})

	o := Overview{Refs: map[string]Reference{l.private.ID: {Repo: repo, Tag: "v2.7.2"}}}
	update := func() Update {
		us := []Update{{Service: l.private, Environment: prod, Running: "v2.7.2", Target: "v3.0.11"}}
		if err := releaseDeltas(ctx, l.st, l.sc, o, us); err != nil {
			t.Fatal(err)
		}
		return us[0]
	}
	_ = l.st.SetPackageVulns(ctx, map[string][]string{brotliOld: {"CVE-2026-111497"}, git: {"CVE-2025-48384"}})
	if u := update(); u.Vulns.Known {
		t.Fatalf("known before OSV answered for the release's packages: %+v", u.Vulns)
	}
	_ = l.st.SetPackageVulns(ctx, map[string][]string{brotliNew: nil})
	u := update()
	if !u.Vulns.Known || !u.Vulns.Unreleased || len(u.Vulns.Fixed) != 1 || u.Vulns.Fixed[0].ID != "CVE-2026-111497" ||
		!u.Vulns.Fixed[0].Exploited || len(u.Vulns.New) != 0 {
		t.Fatalf("delta %+v", u.Vulns)
	}
}

// The releases to read are the versions open upgrades move to that run nowhere yet; managed engines have no image.
func TestReleaseTargets(t *testing.T) {
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	prod := store.Environment{ID: "prod", Name: "production"}
	up := func(svc, running, target string) store.Drift {
		b, _ := json.Marshal(DriftDetail{Running: running, Other: target, Jump: JumpMinor})
		return store.Drift{ServiceID: svc, EnvironmentID: "prod", Kind: "upstream", Detail: b, Since: now}
	}
	o := Overview{
		Services: map[string]store.Service{"px": {ID: "px", Name: "proxysql"}, "db": {ID: "db", Name: "db"}, "web": {ID: "web", Name: "web"}},
		Envs:     map[string]store.Environment{"prod": prod},
		Refs: map[string]Reference{
			"px": {Repo: "1.dkr.ecr.us-west-2.amazonaws.com/proxysql"}, "db": {Repo: "rds.amazonaws.com/postgres"}, "web": {Repo: "docker.io/library/nginx"},
		},
		Drifts: map[string][]store.Drift{
			"px|prod": {up("px", "v2.7.2", "v3.0.11")}, "db|prod": {up("db", "15.6", "16.4")}, "web|prod": {up("web", "1.27.1", "1.27.2")},
		},
	}
	active := []store.Instance{{Image: "nginx:1.27.2", Tag: "1.27.2"}} // runs elsewhere already: its packages are known
	got := releaseTargets(o, active, now)
	if len(got) != 1 || got[0] != (releaseTarget{"px", "1.dkr.ecr.us-west-2.amazonaws.com/proxysql", "v3.0.11"}) {
		t.Fatalf("targets %+v", got)
	}
}
