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

func TestAppsAndTeams(t *testing.T) {
	e := newUIEnv(t)
	ctx := context.Background()
	sc := e.ws.Scope()
	// A second running service, without a team.
	api, _ := e.st.EnsureService(ctx, sc, "api")
	existing, _ := e.st.ListTargetInstances(ctx, sc, e.tgt.ID)
	ch := store.SnapshotChanges{Scope: sc, SnapshotID: store.NewID(), TargetID: e.tgt.ID, At: time.Now(), Upsert: append(existing, store.Instance{
		TargetID: e.tgt.ID, EnvironmentID: e.prod.ID, ServiceID: api.ID, WorkloadID: "api", WorkloadKind: "deployment", WorkloadName: "api",
		ContainerName: "app", Image: "ghcr.io/acme/api:1.0.0", Tag: "1.0.0", Running: 1, IsMain: true,
	})}
	_, _ = e.st.InsertSnapshot(ctx, store.Snapshot{ID: ch.SnapshotID, Scope: sc, TargetID: e.tgt.ID, CollectedAt: time.Now(), Complete: true, Payload: json.RawMessage(`{}`)})
	if err := e.st.ApplySnapshot(ctx, ch); err != nil {
		t.Fatal(err)
	}
	svc := e.serviceOf("evil") // runs in the default fixture without labels: no application
	member, viewer := e.as(store.RoleMember), e.as(store.RoleViewer)

	// A service placed in an application by hand shows up under it.
	_, page := get(t, member, e.srv.URL+"/services/"+url.PathEscape(svc), nil)
	if !strings.Contains(page, `name="app"`) {
		t.Fatal("service page has no application field")
	}
	if _, body, _ := post(t, member, e.srv.URL+"/services/"+url.PathEscape(svc)+"/policy", url.Values{"owner": {""}, "app": {"storefront"}, "kind": {"own"}}); !strings.Contains(body, "Policy saved") {
		t.Fatalf("save: %s", body)
	}
	_, page = get(t, viewer, e.srv.URL+"/apps", nil)
	if !strings.Contains(page, "storefront") || !strings.Contains(page, "set by hand") || strings.Contains(page, `action="/apps/rename"`) {
		t.Fatalf("apps page (viewer): %s", page)
	}

	// Rename it, then give its services a team.
	if _, body, _ := post(t, member, e.srv.URL+"/apps/rename", url.Values{"app": {"storefront"}, "to": {"shop"}}); !strings.Contains(body, "storefront is shown as shop now") {
		t.Fatalf("rename: %s", body)
	}
	if names, _ := e.st.AppNames(ctx, sc); names["storefront"] != "shop" {
		t.Fatalf("names %v", names)
	}
	if _, body, _ := post(t, member, e.srv.URL+"/apps/rename", url.Values{"app": {"shop"}, "to": {"<b>"}}); !strings.Contains(body, "Give the application a name") {
		t.Fatal("a bad name was accepted")
	}
	if _, body, _ := post(t, member, e.srv.URL+"/apps/team", url.Values{"app": {"shop"}, "team": {"team-shop"}}); !strings.Contains(body, "now belong to team-shop") {
		t.Fatalf("app team: %s", body)
	}
	got, _ := e.st.GetServiceByName(ctx, sc, svc)
	if got.Owner != "team-shop" {
		t.Fatalf("owner %q", got.Owner)
	}

	// Teams: listed, renamed; services without a team get one.
	_, page = get(t, member, e.srv.URL+"/teams", nil)
	if !strings.Contains(page, "team-shop") || !strings.Contains(page, "Without a team") {
		t.Fatalf("teams page: %s", page)
	}
	if _, body, _ := post(t, member, e.srv.URL+"/teams/rename", url.Values{"team": {"team-shop"}, "to": {"shop-squad"}}); !strings.Contains(body, "team-shop is shop-squad now") {
		t.Fatalf("team rename: %s", body)
	}
	if _, body, _ := post(t, member, e.srv.URL+"/teams/assign", url.Values{"service": {"api"}, "team": {"platform"}}); !strings.Contains(body, "1 service now belong to platform") {
		t.Fatalf("assign: %s", body)
	}
	if got, _ := e.st.GetServiceByName(ctx, sc, "api"); got.Owner != "platform" {
		t.Fatalf("assigned owner %q", got.Owner)
	}
	if code, _, _ := post(t, viewer, e.srv.URL+"/teams/rename", url.Values{"team": {"platform"}, "to": {"x"}}); code != 403 {
		t.Fatalf("viewer renamed a team: %d", code)
	}
}
