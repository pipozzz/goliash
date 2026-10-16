// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package notifier

import (
	"context"
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

	for range 2 { // once per new image
		if err := e.n.CheckImageGrowth(ctx); err != nil {
			t.Fatal(err)
		}
		_ = e.n.DeliverDue(ctx)
	}
	if slack.count() != 1 {
		t.Fatalf("alerts %d", slack.count())
	}
	for _, want := range []string{"image grew from 90 MB to 240 MB", "+150 MB, +166%", "2.0.0"} {
		if !strings.Contains(slack.last(), want) {
			t.Errorf("alert misses %q: %s", want, slack.last())
		}
	}
}
