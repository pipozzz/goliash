// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package ui

import (
	"context"
	"encoding/json"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/pipozzz/goliash/internal/store"
)

// A shared image mapped under one project's name is renamed after the image, and a
// second such service merges into it with its workloads and history.
func TestRenameAndMergeService(t *testing.T) {
	e := newUIEnv(t)
	ctx := context.Background()
	sc := e.ws.Scope()
	run := func(svcName, workload string) store.Service {
		svc, _ := e.st.EnsureService(ctx, sc, svcName)
		existing, _ := e.st.ListTargetInstances(ctx, sc, e.tgt.ID)
		ch := store.SnapshotChanges{Scope: sc, SnapshotID: store.NewID(), TargetID: e.tgt.ID, At: time.Now(), Upsert: append(existing, store.Instance{
			TargetID: e.tgt.ID, EnvironmentID: e.prod.ID, ServiceID: svc.ID, WorkloadID: workload, WorkloadKind: "deployment", WorkloadName: workload,
			ContainerName: "redis", Image: "redis:7.4.0", Tag: "7.4.0", Running: 1, IsMain: true,
		})}
		_, _ = e.st.InsertSnapshot(ctx, store.Snapshot{ID: ch.SnapshotID, Scope: sc, TargetID: e.tgt.ID, CollectedAt: time.Now(), Complete: true, Payload: json.RawMessage(`{}`)})
		if err := e.st.ApplySnapshot(ctx, ch); err != nil {
			t.Fatal(err)
		}
		return svc
	}
	cefiro := run("cefiro-redis", "cefiro-redis")
	lawrio := run("lawrio-redis", "lawrio-redis")
	if _, err := e.st.CreateMappingRule(ctx, store.MappingRule{Scope: sc, Priority: 50, MatchType: "workload_name", Pattern: "^lawrio-redis$", ServiceID: lawrio.ID}); err != nil {
		t.Fatal(err)
	}
	member := e.as(store.RoleMember)
	if _, body, _ := post(t, member, e.srv.URL+"/services/cefiro-redis/rename", url.Values{"to": {"redis"}}); !strings.Contains(body, "cefiro-redis is redis now") {
		t.Fatalf("rename: %s", body)
	}
	// The name is taken: without merge it says so; with merge the workloads move.
	if _, body, _ := post(t, member, e.srv.URL+"/services/lawrio-redis/rename", url.Values{"to": {"redis"}}); !strings.Contains(body, "exists") {
		t.Fatalf("taken name: %s", body)
	}
	if _, body, _ := post(t, member, e.srv.URL+"/services/lawrio-redis/rename", url.Values{"to": {"redis"}, "merge": {"1"}}); !strings.Contains(body, "lawrio-redis was merged into redis") {
		t.Fatalf("merge: %s", body)
	}
	if _, err := e.st.GetServiceByName(ctx, sc, "lawrio-redis"); err == nil {
		t.Fatal("merged service still there")
	}
	active, _ := e.st.ListActiveInstances(ctx, sc)
	n := 0
	for _, i := range active {
		if i.ServiceID == cefiro.ID {
			n++
		}
	}
	rules, _ := e.st.ListMappingRules(ctx, sc)
	if n != 2 || len(rules) != 1 || rules[0].ServiceID != cefiro.ID {
		t.Fatalf("after merge: %d workloads, rules %+v", n, rules)
	}
	if code, _, _ := post(t, e.as(store.RoleViewer), e.srv.URL+"/services/redis/rename", url.Values{"to": {"x"}}); code != 403 {
		t.Fatalf("viewer renamed: %d", code)
	}
}
