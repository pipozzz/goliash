// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package versions

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pipozzz/goliash/internal/registry"
	"github.com/pipozzz/goliash/internal/store"
	"github.com/pipozzz/goliash/pkg/agentproto"
)

type evidenceTags struct {
	*fakeTags
	lookups atomic.Int32
}

func (e *evidenceTags) ImageEvidence(_ context.Context, _, digest string, _ registry.Credentials) (registry.Evidence, error) {
	e.lookups.Add(1)
	if digest == "sha256:signed" {
		return registry.Evidence{Signed: true, SBOM: true, Found: []string{"cosign signature", "buildkit sbom"}}, nil
	}
	return registry.Evidence{}, nil
}

func TestCheckEvidence(t *testing.T) {
	l := newLab(t)
	ctx := context.Background()
	reg := &evidenceTags{fakeTags: l.tags}
	l.checker = NewChecker(l.st, reg, slog.New(slog.NewTextHandler(io.Discard, nil)), time.Hour)
	// Two public images with digests, one private one (left to the agents), one without a digest.
	for name, ins := range map[string][]store.Instance{
		"prod-a": {
			{WorkloadID: "a", WorkloadName: "a", ContainerName: "nginx", Image: "nginx:1.27.2", Tag: "1.27.2", Digest: "sha256:signed", Running: 1, IsMain: true},
			{WorkloadID: "b", WorkloadName: "b", ContainerName: "web", Image: "ghcr.io/acme/web:2", Tag: "2", Digest: "sha256:plain", Running: 1, IsMain: true},
			{WorkloadID: "c", WorkloadName: "c", ContainerName: "pay", Image: "registry.internal.example/pay:1", Tag: "1", Digest: "sha256:private", Running: 1, IsMain: true},
			{WorkloadID: "d", WorkloadName: "d", ContainerName: "x", Image: "redis:7", Tag: "7", Running: 1, IsMain: true},
		},
	} {
		tgt := l.targets[name]
		for i := range ins {
			ins[i].TargetID, ins[i].EnvironmentID = tgt.ID, tgt.EnvironmentID
		}
		if err := l.st.ApplySnapshot(ctx, store.SnapshotChanges{Scope: l.sc, TargetID: tgt.ID, SnapshotID: store.NewID(), At: time.Now(), Upsert: ins}); err != nil {
			t.Fatal(err)
		}
	}
	if err := l.checker.CheckEvidence(ctx, l.sc); err != nil {
		t.Fatal(err)
	}
	got, _ := l.st.ListImageEvidence(ctx, l.sc)
	if len(got) != 2 || reg.lookups.Load() != 2 {
		t.Fatalf("looked up %d, recorded %+v", reg.lookups.Load(), got)
	}
	if s := got["docker.io/library/nginx@sha256:signed"]; !s.Signed || !s.SBOM || s.Provenance {
		t.Errorf("signed image %+v", s)
	}
	// Fresh lookups are not repeated; a negative one is, after a day.
	_ = l.checker.CheckEvidence(ctx, l.sc)
	if reg.lookups.Load() != 2 {
		t.Errorf("looked up again within the TTL: %d", reg.lookups.Load())
	}
	l.checker.now = func() time.Time { return time.Now().UTC().Add(25 * time.Hour) }
	_ = l.checker.CheckEvidence(ctx, l.sc)
	if reg.lookups.Load() != 3 {
		t.Errorf("after a day, only the unsigned image is looked up again: %d", reg.lookups.Load())
	}
}

// Private images are inspected by the agents: the server asks for running digests without a fresh lookup,
// and stops asking once their answer is recorded.
func TestPrivateInspections(t *testing.T) {
	l := newLab(t)
	ctx := context.Background()
	digest := "sha256:" + strings.Repeat("ab", 32)
	tgt := l.targets["prod-a"]
	if err := l.st.ApplySnapshot(ctx, store.SnapshotChanges{Scope: l.sc, TargetID: tgt.ID, SnapshotID: store.NewID(), At: time.Now(), Upsert: []store.Instance{
		{
			TargetID: tgt.ID, EnvironmentID: tgt.EnvironmentID, ServiceID: l.private.ID, WorkloadID: "pay", WorkloadName: "pay", ContainerName: "app",
			Image: "registry.internal.example/team/payments:1.0.0", Tag: "1.0.0", Digest: digest, Running: 1, IsMain: true,
		},
	}}); err != nil {
		t.Fatal(err)
	}
	inspectOf := func() []string {
		t.Helper()
		checks, err := l.checker.PrivateRepositories(ctx, l.sc)
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range checks {
			if c.Repository == "registry.internal.example/team/payments" {
				return c.Inspect
			}
		}
		t.Fatalf("no check for the private repository: %+v", checks)
		return nil
	}
	if got := inspectOf(); len(got) != 1 || got[0] != digest {
		t.Fatalf("inspect %v", got)
	}
	yes := true
	purls := []string{"pkg:npm/lodash@4.17.15"}
	if err := l.checker.RecordInspections(ctx, l.sc, "registry.internal.example/team/payments", []agentproto.ImageInspection{
		{Digest: digest, Signed: &yes, Sbom: &yes, Found: []string{"cosign signature", "buildkit sbom"}, Purls: purls},
	}); err != nil {
		t.Fatal(err)
	}
	if got := inspectOf(); len(got) != 0 {
		t.Fatalf("asked again after the answer: %v", got)
	}
	ev, _ := l.st.ListImageEvidence(ctx, l.sc)
	sb, _ := l.st.ListImageSBOMs(ctx, l.sc)
	key := "registry.internal.example/team/payments@" + digest
	if !ev[key].Signed || !ev[key].SBOM || len(sb[key].Purls) != 1 {
		t.Fatalf("evidence %+v sbom %+v", ev[key], sb[key])
	}
}
