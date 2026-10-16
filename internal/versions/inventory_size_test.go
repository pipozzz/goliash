// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package versions

import (
	"context"
	"testing"
	"time"

	"github.com/pipozzz/goliash/internal/store"
)

// The inventory carries what Goliash read about each image: its size and whether its base is current.
func TestInventorySizeAndBase(t *testing.T) {
	l := newLab(t)
	ctx := context.Background()
	tgt := l.targets["prod-a"]
	in := store.Instance{
		TargetID: tgt.ID, EnvironmentID: tgt.EnvironmentID, ServiceID: l.svc.ID, WorkloadID: "w", WorkloadName: "web",
		ContainerName: "web", Image: "ghcr.io/acme/web:2", Tag: "2", Digest: "sha256:d", Running: 1, IsMain: true,
	}
	if err := l.st.ApplySnapshot(ctx, store.SnapshotChanges{Scope: l.sc, TargetID: tgt.ID, SnapshotID: store.NewID(), At: time.Now(), Upsert: []store.Instance{in}}); err != nil {
		t.Fatal(err)
	}
	if err := l.st.SetImageEvidence(ctx, l.sc, store.ImageEvidence{
		Repo: "ghcr.io/acme/web", Digest: "sha256:d", Size: 76_000_000, BaseImage: "docker.io/library/node:22-alpine", BaseStatus: "outdated",
	}); err != nil {
		t.Fatal(err)
	}
	items, err := Inventory(ctx, l.st, l.sc, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range items {
		if it.Digest == "sha256:d" {
			if it.SizeBytes != 76_000_000 || it.BaseStatus != "outdated" || it.Base != "docker.io/library/node:22-alpine" {
				t.Errorf("item %+v", it)
			}
			return
		}
	}
	t.Fatal("the image is not in the inventory")
}
