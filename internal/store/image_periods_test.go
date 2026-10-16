// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"context"
	"testing"
	"time"
)

func TestImagePeriods(t *testing.T) {
	forEachDialect(t, func(t *testing.T, s *Store) {
		ctx := context.Background()
		f := setup(t, s)
		sc := f.ws.Scope()
		svc, err := s.EnsureService(ctx, sc, "api")
		if err != nil {
			t.Fatal(err)
		}
		in := func(digest string) Instance {
			return Instance{TargetID: f.tgt.ID, EnvironmentID: f.env.ID, ServiceID: svc.ID, WorkloadID: "w", WorkloadName: "api",
				ContainerName: "api", Image: "ghcr.io/acme/api:" + digest[7:], Tag: digest[7:], Digest: digest, Running: 1, IsMain: true}
		}
		now := time.Now()
		if err := s.ApplySnapshot(ctx, SnapshotChanges{Scope: sc, SnapshotID: NewID(), TargetID: f.tgt.ID, At: now.Add(-48 * time.Hour), Upsert: []Instance{in("sha256:1")}}); err != nil {
			t.Fatal(err)
		}
		active, _ := s.ListActiveInstances(ctx, sc)
		if err := s.ApplySnapshot(ctx, SnapshotChanges{Scope: sc, SnapshotID: NewID(), TargetID: f.tgt.ID, At: now.Add(-time.Hour), Upsert: []Instance{in("sha256:2")}, Remove: []string{active[0].ID}}); err != nil {
			t.Fatal(err)
		}
		all, err := s.ImagePeriods(ctx, sc, now.Add(-72*time.Hour))
		if err != nil || len(all) != 2 || all[0].Digest != "sha256:1" || all[0].Until.IsZero() || !all[1].Until.IsZero() || all[1].Tag != "2" || all[1].ServiceID != svc.ID {
			t.Fatalf("periods %+v %v", all, err)
		}
		one, err := s.ServiceImagePeriods(ctx, sc, svc.ID, now.Add(-30*time.Minute))
		if err != nil || len(one) != 1 || one[0].Digest != "sha256:2" {
			t.Fatalf("since: %+v %v", one, err)
		}
	})
}
