// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package versions

import (
	"context"
	"testing"
	"time"

	"github.com/pipozzz/goliash/internal/store"
)

func TestRolloutIssues(t *testing.T) {
	l := newLab(t)
	ctx := context.Background()
	three, two := 3, 2
	put := func(target, workload, tag string, running int, desired *int, at time.Time) {
		t.Helper()
		tg := l.targets[target]
		if err := l.st.ApplySnapshot(ctx, store.SnapshotChanges{Scope: l.sc, TargetID: tg.ID, SnapshotID: store.NewID(), At: at, Upsert: []store.Instance{{
			TargetID: tg.ID, EnvironmentID: tg.EnvironmentID, ServiceID: l.svc.ID, WorkloadID: workload, WorkloadName: workload, ContainerName: "nginx",
			Image: "nginx:" + tag, Tag: tag, Digest: "sha256:" + tag, Running: running, Desired: desired, IsMain: true,
		}}}); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now()
	// prod-a: 1.28.0 on 1 of 3 for an hour, 1.27.2 still on 2: stuck.
	put("prod-a", "web", "1.27.2", 2, &three, now.Add(-48*time.Hour))
	put("prod-a", "web", "1.28.0", 1, &three, now.Add(-time.Hour))
	// prod-b: nothing runs of 2 asked for: down.
	put("prod-b", "web", "1.27.2", 0, &two, now.Add(-2*time.Hour))
	// stg-1: 2 of 3, for days: short.
	put("stg-1", "web", "1.28.0", 2, &three, now.Add(-72*time.Hour))
	// dev-1: a rollout started five minutes ago: not yet anything.
	put("dev-1", "web", "1.27.2", 1, &two, now.Add(-24*time.Hour))
	put("dev-1", "web", "1.28.0", 1, &two, now.Add(-5*time.Minute))
	// dev-1 cron job: no desired replicas, never counts.
	put("dev-1", "nightly", "1.28.0", 0, nil, now.Add(-24*time.Hour))

	o, err := LoadOverview(ctx, l.st, l.sc)
	if err != nil {
		t.Fatal(err)
	}
	issues, err := RolloutIssues(ctx, l.st, l.sc, o, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(issues) != 3 {
		t.Fatalf("issues %+v", issues)
	}
	down, stuck, short := issues[0], issues[1], issues[2]
	if down.Kind != RolloutDown || down.Target != "prod-b" || down.Desired != 2 || down.Running != 0 {
		t.Errorf("down %+v", down)
	}
	if stuck.Kind != RolloutStuck || stuck.Target != "prod-a" || stuck.Running != 3 || len(stuck.Versions) != 2 ||
		stuck.Versions[0].Version != "1.28.0" || stuck.Versions[0].Running != 1 || stuck.Environment != "prod" {
		t.Errorf("stuck %+v", stuck)
	}
	if short.Kind != RolloutShort || short.Target != "stg-1" || short.Running != 2 || short.Desired != 3 {
		t.Errorf("short %+v", short)
	}
}
