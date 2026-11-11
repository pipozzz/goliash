// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package ui

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/pipozzz/goliash/internal/store"
)

func TestInfrastructureMap(t *testing.T) {
	e := newUIEnv(t)
	ctx := t.Context()
	sc := e.ws.Scope()
	ecs, err := e.st.CreateTarget(ctx, store.Target{
		Scope: sc, EnvironmentID: e.prod.ID, Platform: "ecs", Name: "shop-ecs",
		Settings: json.RawMessage(`{"ecs": {"region": "eu-west-1", "clusters": ["arn:aws:ecs:eu-west-1:123456789012:cluster/shop"]}}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.CreateTarget(ctx, store.Target{Scope: sc, EnvironmentID: e.prod.ID, Platform: "docker", Name: "basement-box"}); err != nil {
		t.Fatal(err)
	}
	snap := store.NewID()
	_, _ = e.st.InsertSnapshot(ctx, store.Snapshot{ID: snap, Scope: sc, TargetID: ecs.ID, CollectedAt: time.Now(), Complete: true, Payload: json.RawMessage(`{}`)})
	if err := e.st.ApplySnapshot(ctx, store.SnapshotChanges{Scope: sc, SnapshotID: snap, TargetID: ecs.ID, At: time.Now(), Upsert: []store.Instance{{
		TargetID: ecs.ID, EnvironmentID: e.prod.ID, WorkloadID: "w1", WorkloadKind: "ecs_service", WorkloadName: "shop",
		ContainerName: "shop", Image: "ghcr.io/acme/shop:1.0.0", Tag: "1.0.0", Running: 2, IsMain: true,
	}}}); err != nil {
		t.Fatal(err)
	}
	code, page := get(t, e.as(store.RoleViewer), e.srv.URL+"/infrastructure", nil)
	if code != 200 {
		t.Fatalf("infrastructure %d", code)
	}
	for _, want := range []string{
		"AWS", "123456789012", "eu-west-1", `href="/targets/` + ecs.ID + `"`, "shop-ecs", "1 unmapped",
		"Outside a cloud", "basement-box", "nothing running", `class="infra-tile unmapped"`,
	} {
		if !strings.Contains(page, want) {
			t.Errorf("infrastructure page misses %q", want)
		}
	}
	// The cloud comes before the machines outside one.
	if strings.Index(page, "AWS") > strings.Index(page, "Outside a cloud") {
		t.Error("outside a cloud listed first")
	}
}
