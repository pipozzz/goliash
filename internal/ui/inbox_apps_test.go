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

// Four projects each run a workload named "db" on postgres: each maps to a service of
// its own, named with its project.
func TestInboxSameNameInSeveralApps(t *testing.T) {
	e := newUIEnv(t)
	ctx := context.Background()
	sc := e.ws.Scope()
	existing, _ := e.st.ListTargetInstances(ctx, sc, e.tgt.ID)
	ch := store.SnapshotChanges{Scope: sc, SnapshotID: store.NewID(), TargetID: e.tgt.ID, At: time.Now(), Upsert: existing}
	for _, app := range []string{"velin-lawrio", "velin-portal"} {
		ch.Upsert = append(ch.Upsert, store.Instance{
			TargetID: e.tgt.ID, EnvironmentID: e.prod.ID, WorkloadID: app + "/db", WorkloadKind: "compose_service", WorkloadName: "db",
			ContainerName: "db", Image: "postgres:17", Tag: "17", Running: 1, IsMain: true, SuggestedService: "postgres",
			// As the Docker collector reports it: the project as the namespace, no compose labels.
			Namespace: app + "-ysp9bd", App: app, AppSource: "compose project",
		})
	}
	_, _ = e.st.InsertSnapshot(ctx, store.Snapshot{ID: ch.SnapshotID, Scope: sc, TargetID: e.tgt.ID, CollectedAt: time.Now(), Complete: true, Payload: json.RawMessage(`{}`)})
	if err := e.st.ApplySnapshot(ctx, ch); err != nil {
		t.Fatal(err)
	}
	member := e.as(store.RoleMember)
	_, page := get(t, member, e.srv.URL+"/inbox", nil)
	if !strings.Contains(page, `value="velin-lawrio-db"`) || !strings.Contains(page, `name="app" value="velin-portal"`) ||
		!strings.Contains(page, "In 2 applications (velin-lawrio, velin-portal)") {
		t.Fatalf("inbox does not offer per-project services: %s", page)
	}
	form := url.Values{"repo": {"docker.io/library/postgres"}, "only": {"workload"}, "workload_name": {"db"}, "app": {"velin-lawrio"}, "service": {"lawrio-db"}}
	if _, body, _ := post(t, member, e.srv.URL+"/inbox/map", form); !strings.Contains(body, "the same name in other applications stays apart") {
		t.Fatalf("map: %s", body)
	}
	active, _ := e.st.ListActiveInstances(ctx, sc)
	got := map[string]string{}
	for _, i := range active {
		if i.WorkloadName == "db" {
			got[i.App] = i.ServiceID
		}
	}
	lawrio, _ := e.st.GetServiceByName(ctx, sc, "lawrio-db")
	if got["velin-lawrio"] != lawrio.ID || got["velin-portal"] != "" {
		t.Fatalf("mapped %v", got)
	}
	rules, _ := e.st.ListMappingRules(ctx, sc)
	if len(rules) != 1 || rules[0].MatchType != "app_workload" {
		t.Fatalf("rules %+v", rules)
	}
}
