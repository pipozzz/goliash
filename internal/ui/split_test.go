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

	"github.com/pipozzz/goliash/internal/mapping"
	"github.com/pipozzz/goliash/internal/store"
	"github.com/pipozzz/goliash/internal/versions"
)

// "db" of mattermost and of portal both landed in chat-db through a rule on the
// name alone; splitting by application gives portal's its own service for good.
func TestSplitServiceByApplication(t *testing.T) {
	e := newUIEnv(t)
	ctx := context.Background()
	sc := e.ws.Scope()
	chat, _ := e.st.EnsureService(ctx, sc, "chat-db")
	if _, err := e.st.CreateMappingRule(ctx, store.MappingRule{
		Scope: sc, Priority: 50, MatchType: "workload_name",
		Pattern: mapping.WorkloadPattern("db"), ServiceID: chat.ID,
	}); err != nil {
		t.Fatal(err)
	}
	existing, _ := e.st.ListTargetInstances(ctx, sc, e.tgt.ID)
	ch := store.SnapshotChanges{Scope: sc, SnapshotID: store.NewID(), TargetID: e.tgt.ID, At: time.Now(), Upsert: existing}
	for _, app := range []string{"mattermost", "portal"} {
		ch.Upsert = append(ch.Upsert, store.Instance{
			ID: store.NewID(), TargetID: e.tgt.ID, EnvironmentID: e.prod.ID, WorkloadID: app + "/db", WorkloadKind: "compose_service",
			WorkloadName: "db", ContainerName: "db", Image: "postgres:17", Tag: "17", Running: 1, IsMain: true,
			ServiceID: chat.ID, Namespace: app, App: app, AppSource: "compose project",
		})
	}
	_, _ = e.st.InsertSnapshot(ctx, store.Snapshot{ID: ch.SnapshotID, Scope: sc, TargetID: e.tgt.ID, CollectedAt: time.Now(), Complete: true, Payload: json.RawMessage(`{}`)})
	if err := e.st.ApplySnapshot(ctx, ch); err != nil {
		t.Fatal(err)
	}
	member := e.as(store.RoleMember)
	_, page := get(t, member, e.srv.URL+"/services/chat-db", nil)
	if !strings.Contains(page, "Split by application") || !strings.Contains(page, `value="portal-db"`) || !strings.Contains(page, `name="name" value="chat-db"`) {
		t.Fatalf("service page: %s", page)
	}
	form := url.Values{"app": {"mattermost", "portal"}, "name": {"chat-db", "portal-db"}}
	if _, body, _ := post(t, member, e.srv.URL+"/services/chat-db/split", form); !strings.Contains(body, "portal → portal-db") {
		t.Fatalf("split: %s", body)
	}
	portal, err := e.st.GetServiceByName(ctx, sc, "portal-db")
	if err != nil {
		t.Fatal(err)
	}
	active, _ := e.st.ListActiveInstances(ctx, sc)
	got := map[string]string{}
	for _, i := range active {
		if i.WorkloadName == "db" {
			got[i.App] = i.ServiceID
		}
	}
	if got["mattermost"] != chat.ID || got["portal"] != portal.ID {
		t.Fatalf("instances %v", got)
	}
	// The next snapshots map there too: the application's rule wins over the name-only one.
	rules, _ := e.st.ListMappingRules(ctx, sc)
	m, _ := mapping.New(rules)
	ref := versions.ParseImage("postgres:17")
	if d := m.Map(mapping.Workload{Name: "db", App: "portal"}, "db", ref); d.ServiceID != portal.ID {
		t.Fatalf("portal maps to %s", d.ServiceID)
	}
	if d := m.Map(mapping.Workload{Name: "db", App: "mattermost"}, "db", ref); d.ServiceID != chat.ID {
		t.Fatalf("mattermost maps to %s", d.ServiceID)
	}
	if _, page := get(t, member, e.srv.URL+"/services/chat-db", nil); strings.Contains(page, "Split by application") {
		t.Fatal("still offers a split")
	}
}
