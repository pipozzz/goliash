// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package versions

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/pipozzz/goliash/internal/registry"
	"github.com/pipozzz/goliash/internal/store"
)

// baseTags plays a registry where node:22-alpine was rebuilt on 2026-10-02.
type baseTags struct {
	*fakeTags
	baseReads int
}

func (b *baseTags) ImageEvidence(_ context.Context, _, digest string, _ registry.Credentials) (registry.Evidence, error) {
	switch digest {
	case "sha256:fresh":
		return registry.Evidence{Layers: []string{"sha256:alpine", "sha256:node-oct", "sha256:app"}}, nil
	case "sha256:stale":
		return registry.Evidence{Layers: []string{"sha256:alpine", "sha256:node-aug", "sha256:app"}}, nil
	}
	return registry.Evidence{}, nil
}

func (b *baseTags) CurrentBase(_ context.Context, repo, tag string, _ registry.Credentials) (registry.Base, error) {
	b.baseReads++
	if repo != "docker.io/library/node" || tag != "22-alpine" {
		return registry.Base{}, registry.ErrNotFound
	}
	return registry.Base{Layers: []string{"sha256:alpine", "sha256:node-oct"}, Created: time.Date(2026, 10, 2, 8, 0, 0, 0, time.UTC)}, nil
}

func TestOutdatedBase(t *testing.T) {
	l := newLab(t)
	ctx := context.Background()
	reg := &baseTags{fakeTags: l.tags}
	l.checker = NewChecker(l.st, reg, slog.New(slog.NewTextHandler(io.Discard, nil)), time.Hour)
	api, _ := l.st.EnsureService(ctx, l.sc, "api")
	for _, s := range []store.Service{l.svc, api} {
		if err := l.st.SetServiceSource(ctx, l.sc, s.ID, "", "", "node:22-alpine"); err != nil {
			t.Fatal(err)
		}
	}
	tgt := l.targets["prod-a"]
	ins := []store.Instance{
		{ServiceID: l.svc.ID, WorkloadID: "a", WorkloadName: "web", ContainerName: "web", Image: "ghcr.io/acme/web:2", Tag: "2", Digest: "sha256:fresh", Running: 1, IsMain: true},
		{ServiceID: api.ID, WorkloadID: "b", WorkloadName: "api", ContainerName: "api", Image: "ghcr.io/acme/api:5", Tag: "5", Digest: "sha256:stale", Running: 1, IsMain: true},
	}
	for i := range ins {
		ins[i].TargetID, ins[i].EnvironmentID = tgt.ID, tgt.EnvironmentID
	}
	if err := l.st.ApplySnapshot(ctx, store.SnapshotChanges{Scope: l.sc, TargetID: tgt.ID, SnapshotID: store.NewID(), At: time.Now(), Upsert: ins}); err != nil {
		t.Fatal(err)
	}
	if err := l.checker.CheckEvidence(ctx, l.sc); err != nil {
		t.Fatal(err)
	}
	got, _ := l.st.ListImageEvidence(ctx, l.sc)
	if f := got["ghcr.io/acme/web@sha256:fresh"]; f.BaseStatus != "current" || f.BaseImage != "docker.io/library/node:22-alpine" {
		t.Errorf("fresh: %+v", f)
	}
	if s := got["ghcr.io/acme/api@sha256:stale"]; s.BaseStatus != "outdated" || s.BaseSince.IsZero() {
		t.Errorf("stale: %+v", s)
	}
	if reg.baseReads != 1 {
		t.Errorf("the base was read %d times in one run", reg.baseReads)
	}

	// The security posture names the production service built on an outdated base.
	o, err := LoadOverview(ctx, l.st, l.sc)
	if err != nil {
		t.Fatal(err)
	}
	pos, err := LoadPosture(ctx, l.st, l.sc, o, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(pos.OutdatedBase) != 1 || pos.OutdatedBase[0] != "api" {
		t.Errorf("posture outdated bases: %v (prod %s)", pos.OutdatedBase, pos.Prod.Name)
	}

	findings, err := LoadHygiene(ctx, l.st, l.sc)
	if err != nil {
		t.Fatal(err)
	}
	var outdated []Finding
	for _, f := range findings {
		if f.Kind == "outdated-base" {
			outdated = append(outdated, f)
		}
	}
	if len(outdated) != 1 || outdated[0].Image != "ghcr.io/acme/api:5" || !strings.Contains(outdated[0].Detail, "older node:22-alpine") ||
		!strings.Contains(outdated[0].Detail, "Oct 2") || !strings.Contains(outdated[0].Detail, "--pull") {
		t.Fatalf("hygiene: %+v", outdated)
	}
}
