// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package versions

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/pipozzz/goliash/internal/registry"
	"github.com/pipozzz/goliash/internal/store"
)

// chainTags plays registries where alpine:3.20 was rebuilt, ghcr.io/acme/base:1 (labelled) was not rebuilt on it,
// and ghcr.io/acme/tools:2 (no label, BuildKit provenance) was built on the old alpine with a golang builder stage.
type chainTags struct {
	*fakeTags
	reads map[string]int
}

func (c *chainTags) ImageEvidence(_ context.Context, _, digest string, _ registry.Credentials) (registry.Evidence, error) {
	switch digest {
	case "sha256:web":
		return registry.Evidence{Layers: []string{"sha256:alpine-old", "sha256:base", "sha256:web"}}, nil
	case "sha256:cli":
		return registry.Evidence{Layers: []string{"sha256:alpine-old", "sha256:tools", "sha256:cli"}}, nil
	}
	return registry.Evidence{}, nil
}

func (c *chainTags) CurrentBase(_ context.Context, repo, ref string, _ registry.Credentials) (registry.Base, error) {
	c.reads[repo+":"+ref]++
	at := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	switch repo + "|" + ref {
	case "ghcr.io/acme/base|1":
		return registry.Base{Layers: []string{"sha256:alpine-old", "sha256:base"}, Created: at}, nil
	case "ghcr.io/acme/tools|2":
		return registry.Base{Layers: []string{"sha256:alpine-old", "sha256:tools"}, Created: at}, nil
	case "docker.io/library/alpine|3.20":
		return registry.Base{Layers: []string{"sha256:alpine-new"}, Created: time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC)}, nil
	case "docker.io/library/alpine|sha256:old":
		return registry.Base{Layers: []string{"sha256:alpine-old"}}, nil
	case "docker.io/library/golang|1.23", "docker.io/library/golang|sha256:builder":
		return registry.Base{Layers: []string{"sha256:debian", "sha256:go"}}, nil
	}
	return registry.Base{}, registry.ErrNotFound
}

func (c *chainTags) ImageMeta(_ context.Context, repo, _ string, _ registry.Credentials) (registry.ImageMeta, error) {
	if repo == "ghcr.io/acme/base" {
		return registry.ImageMeta{Base: "docker.io/library/alpine:3.20"}, nil
	}
	return registry.ImageMeta{}, nil
}

func (c *chainTags) BuildBases(_ context.Context, repo, _ string, _ registry.Credentials) ([]string, error) {
	if repo == "ghcr.io/acme/tools" {
		return []string{"golang:1.23@sha256:builder", "alpine:3.20@sha256:old"}, nil
	}
	return nil, nil
}

func TestBaseChain(t *testing.T) {
	l := newLab(t)
	ctx := context.Background()
	reg := &chainTags{fakeTags: l.tags, reads: map[string]int{}}
	l.checker = NewChecker(l.st, reg, slog.New(slog.NewTextHandler(io.Discard, nil)), time.Hour)
	cli, _ := l.st.EnsureService(ctx, l.sc, "cli")
	if err := l.st.SetServiceSource(ctx, l.sc, l.svc.ID, "", "", "ghcr.io/acme/base:1"); err != nil {
		t.Fatal(err)
	}
	if err := l.st.SetServiceSource(ctx, l.sc, cli.ID, "", "", "ghcr.io/acme/tools:2"); err != nil {
		t.Fatal(err)
	}
	tgt := l.targets["prod-a"]
	ins := []store.Instance{
		{ServiceID: l.svc.ID, WorkloadID: "a", WorkloadName: "web", ContainerName: "web", Image: "ghcr.io/acme/web:7", Tag: "7", Digest: "sha256:web", Running: 1, IsMain: true},
		{ServiceID: cli.ID, WorkloadID: "b", WorkloadName: "cli", ContainerName: "cli", Image: "ghcr.io/acme/cli:3", Tag: "3", Digest: "sha256:cli", Running: 1, IsMain: true},
	}
	for i := range ins {
		ins[i].TargetID, ins[i].EnvironmentID = tgt.ID, tgt.EnvironmentID
	}
	if err := l.st.ApplySnapshot(ctx, store.SnapshotChanges{Scope: l.sc, TargetID: tgt.ID, SnapshotID: store.NewID(), At: time.Now(), Upsert: ins}); err != nil {
		t.Fatal(err)
	}
	// The workspace reads ghcr.io.
	if err := l.st.SetSecretKey(bytes.Repeat([]byte{3}, store.SecretKeySize)); err != nil {
		t.Fatal(err)
	}
	if _, err := l.st.SetRegistryCredential(ctx, store.RegistryCredential{Scope: l.sc, Host: "ghcr.io", Username: "u", Secret: "p"}); err != nil {
		t.Fatal(err)
	}
	if err := l.checker.CheckEvidence(ctx, l.sc); err != nil {
		t.Fatal(err)
	}
	got, _ := l.st.ListImageEvidence(ctx, l.sc)

	web := got["ghcr.io/acme/web@sha256:web"]
	if web.BaseStatus != "outdated" || ChainText(web.BaseChain) != "ghcr.io/acme/base:1 (current) → alpine:3.20 (outdated)" {
		t.Fatalf("web: %s, chain %s", web.BaseStatus, ChainText(web.BaseChain))
	}
	if web.BaseChain[1].How != "label" || !web.BaseSince.Equal(time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("web link %+v, since %v", web.BaseChain[1], web.BaseSince)
	}
	if stale, rebuild := OutdatedBase(web); stale != "docker.io/library/alpine:3.20" || rebuild != "ghcr.io/acme/base:1" {
		t.Errorf("OutdatedBase = %s, %s", stale, rebuild)
	}

	cliEv := got["ghcr.io/acme/cli@sha256:cli"]
	if ChainText(cliEv.BaseChain) != "ghcr.io/acme/tools:2 (current) → alpine:3.20 (outdated)" || cliEv.BaseChain[1].How != "provenance" {
		t.Fatalf("cli chain %+v", cliEv.BaseChain)
	}

	findings, err := LoadHygiene(ctx, l.st, l.sc)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, f := range findings {
		if f.Kind == "outdated-base" && f.Image == "ghcr.io/acme/web:7" {
			found = true
			if !strings.Contains(f.Detail, "built on ghcr.io/acme/base:1, which is built on an older alpine:3.20") || !strings.Contains(f.Detail, "rebuild ghcr.io/acme/base:1 with --pull, then this image") {
				t.Errorf("hygiene: %s", f.Detail)
			}
		}
	}
	if !found {
		t.Error("no outdated-base finding for web")
	}
	if reg.reads["docker.io/library/alpine:3.20"] != 1 {
		t.Errorf("alpine read %d times in one run", reg.reads["docker.io/library/alpine:3.20"])
	}
}
