// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package notifier

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/pipozzz/goliash/internal/store"
)

func TestImageGrowth(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	slack := newSink(t)
	e.rule(e.channel("slack", "ops", map[string]any{"url": slack.srv.URL}), "instant", nil, Filter{})
	tgt, err := e.st.CreateTarget(ctx, store.Target{Scope: e.sc, EnvironmentID: e.prod.ID, Platform: "kubernetes", Name: "k8s"})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	e.clock = now
	run := func(digest, tag string, at time.Time, remove []string) string {
		in := store.Instance{
			TargetID: tgt.ID, EnvironmentID: e.prod.ID, ServiceID: e.svc.ID, WorkloadID: "w", WorkloadName: "api",
			ContainerName: "api", Image: "ghcr.io/acme/api:" + tag, Tag: tag, Digest: digest, Running: 1, IsMain: true,
		}
		if err := e.st.ApplySnapshot(ctx, store.SnapshotChanges{Scope: e.sc, SnapshotID: store.NewID(), TargetID: tgt.ID, At: at, Upsert: []store.Instance{in}, Remove: remove}); err != nil {
			t.Fatal(err)
		}
		active, _ := e.st.ListActiveInstances(ctx, e.sc)
		for _, a := range active {
			if a.Digest == digest {
				return a.ID
			}
		}
		return ""
	}
	size := func(digest string, n int64) {
		if err := e.st.SetImageEvidence(ctx, e.sc, store.ImageEvidence{Repo: "ghcr.io/acme/api", Digest: digest, Size: n}); err != nil {
			t.Fatal(err)
		}
	}
	v1 := run("sha256:01", "1.0.0", now.AddDate(0, 0, -20), nil)
	size("sha256:01", 80_000_000)
	// A small growth is no news.
	v11 := run("sha256:02", "1.1.0", now.AddDate(0, 0, -10), []string{v1})
	size("sha256:02", 90_000_000)
	// A big one, two days ago.
	run("sha256:03", "2.0.0", now.AddDate(0, 0, -2), []string{v11})
	size("sha256:03", 240_000_000)
	// The layers say what grew.
	base := store.ImageLayer{Digest: "sha256:base", Size: 60_000_000, Command: "ADD rootfs /"}
	_ = e.st.SetImageEvidence(ctx, e.sc, store.ImageEvidence{
		Repo: "ghcr.io/acme/api", Digest: "sha256:02", Size: 90_000_000,
		Layers: []store.ImageLayer{base, {Digest: "sha256:app1", Size: 30_000_000, Command: "COPY . /app"}},
	})
	_ = e.st.SetImageEvidence(ctx, e.sc, store.ImageEvidence{
		Repo: "ghcr.io/acme/api", Digest: "sha256:03", Size: 240_000_000,
		Layers: []store.ImageLayer{base, {Digest: "sha256:chrome", Size: 145_000_000, Command: "RUN apt-get install -y chromium"}, {Digest: "sha256:app2", Size: 35_000_000, Command: "COPY . /app"}},
	})

	for range 2 { // once per new image
		if err := e.n.CheckImages(ctx); err != nil {
			t.Fatal(err)
		}
		_ = e.n.DeliverDue(ctx)
	}
	if slack.count() != 1 {
		t.Fatalf("alerts %d", slack.count())
	}
	for _, want := range []string{"image grew from 90 MB to 240 MB", "+150 MB, +166%", "2.0.0", "biggest new layer 145 MB: RUN apt-get install -y chromium"} {
		if !strings.Contains(slack.last(), want) {
			t.Errorf("alert misses %q: %s", want, slack.last())
		}
	}

	// The same deploy, seen again with more known: a 200 MB budget, the evidence 1.1.0 had and 2.0.0 lacks, and a
	// base three weeks behind. Each is announced once.
	pol, _ := json.Marshal(map[string]any{"max_size_mb": 200})
	e.svc.VersionPolicy = pol
	if err := e.st.UpdateService(ctx, e.svc); err != nil {
		t.Fatal(err)
	}
	_ = e.st.SetImageEvidence(ctx, e.sc, store.ImageEvidence{Repo: "ghcr.io/acme/api", Digest: "sha256:02", Size: 90_000_000, Signed: true, SBOM: true})
	_ = e.st.SetImageEvidence(ctx, e.sc, store.ImageEvidence{
		Repo: "ghcr.io/acme/api", Digest: "sha256:03", Size: 240_000_000, BaseImage: "docker.io/library/node:22-alpine",
		BaseStatus: "outdated", BaseSince: now.AddDate(0, 0, -21),
	})
	before := slack.count()
	for range 2 {
		if err := e.n.CheckImages(ctx); err != nil {
			t.Fatal(err)
		}
		_ = e.n.DeliverDue(ctx)
	}
	all := strings.Join(slack.all()[before:], "\n") // due together, they may come as one message
	if slack.count() == before {
		t.Fatal("no new alerts")
	}
	after := slack.count()
	_ = e.n.CheckImages(ctx)
	_ = e.n.DeliverDue(ctx)
	if slack.count() != after {
		t.Error("announced again")
	}
	for _, want := range []string{
		"2.0.0 image is 240 MB, over its 200 MB budget",
		"2.0.0 has no signature, SBOM, though 1.1.0 had",
		"2.0.0 is built on an older node:22-alpine than its tag has had for 21 days: rebuild with --pull",
	} {
		if !strings.Contains(all, want) {
			t.Errorf("alerts miss %q:\n%s", want, all)
		}
	}
}
