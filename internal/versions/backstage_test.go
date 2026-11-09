// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package versions

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/pipozzz/goliash/internal/store"
)

// The catalog is read page by page with the token, owners become teams, and it is kept an hour.
func TestBackstageComponents(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("Authorization") != "Bearer tok" || r.URL.Path != "/api/catalog/entities/by-query" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.URL.Query().Get("cursor") == "" {
			if r.URL.Query().Get("filter") != "kind=component" {
				t.Errorf("filter %q", r.URL.Query().Get("filter"))
			}
			_, _ = w.Write([]byte(`{"items":[{"metadata":{"name":"payments-api","annotations":{"backstage.io/kubernetes-id":"payments"}},"spec":{"owner":"group:default/team-payments"}},
				{"metadata":{"name":"blog"},"spec":{"owner":"user:alice"}}],"pageInfo":{"nextCursor":"c2"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"items":[{"metadata":{"name":"Checkout","annotations":{"goliash.dev/service":"cart"}},"spec":{"owner":"team-shop"}}],"pageInfo":{}}`))
	}))
	defer srv.Close()
	b := NewBackstage(srv.URL+"/", "tok")
	now := time.Now()
	b.now = func() time.Time { return now }
	comps, err := b.Components(context.Background())
	if err != nil || len(comps) != 3 {
		t.Fatalf("%v %v", comps, err)
	}
	want := []CatalogComponent{
		{Name: "payments-api", KubernetesID: "payments", Team: "team-payments"},
		{Name: "blog"},
		{Name: "Checkout", Service: "cart", Team: "team-shop"},
	}
	for i := range want {
		if comps[i] != want[i] {
			t.Errorf("%d: %+v, want %+v", i, comps[i], want[i])
		}
	}
	_, _ = b.Components(context.Background())
	if calls != 2 {
		t.Fatalf("read again within the hour: %d calls", calls)
	}
	now = now.Add(2 * time.Hour)
	_, _ = b.Components(context.Background())
	if calls != 4 {
		t.Fatalf("not read again after an hour: %d calls", calls)
	}
}

// A Backstage component's team comes after the workloads' label and before CODEOWNERS; it matches the service by
// annotation, name or a workload named by kubernetes-id.
func TestOwnersFromBackstage(t *testing.T) {
	l := newLab(t)
	ctx := context.Background()
	tg := l.targets["prod-a"]
	cart, _ := l.st.EnsureService(ctx, l.sc, "cart")
	pay, _ := l.st.EnsureService(ctx, l.sc, "payments-svc")
	inst := func(svc store.Service, workload, team string) store.Instance {
		return store.Instance{
			TargetID: tg.ID, EnvironmentID: tg.EnvironmentID, ServiceID: svc.ID, WorkloadID: workload,
			WorkloadKind: "deployment", WorkloadName: workload, ContainerName: "app", Image: "nginx:1.27.2", Tag: "1.27.2",
			Running: 1, IsMain: true, Team: team,
		}
	}
	if err := l.st.ApplySnapshot(ctx, store.SnapshotChanges{
		Scope: l.sc, TargetID: tg.ID, SnapshotID: store.NewID(), At: time.Now(),
		Upsert: []store.Instance{inst(l.svc, "web", "label-team"), inst(cart, "cart", ""), inst(pay, "payments", "")},
	}); err != nil {
		t.Fatal(err)
	}
	src := OwnerSources{
		Catalog: func(context.Context) ([]CatalogComponent, error) {
			return []CatalogComponent{
				{Name: l.svc.Name, Team: "catalog-team"},
				{Name: "Checkout", Service: "cart", Team: "team-shop"},
				{Name: "payments-api", KubernetesID: "payments", Team: "team-payments"},
			}, nil
		},
		CodeOwners: func(context.Context, string, string, string) (string, error) { return "from-codeowners", nil },
	}
	if _, err := FillOwners(ctx, l.st, l.sc, src); err != nil {
		t.Fatal(err)
	}
	owner := func(s store.Service) string {
		got, _ := l.st.GetService(ctx, l.sc, s.ID)
		return got.Owner + "/" + got.OwnerSource
	}
	if got := owner(l.svc); got != "label-team/label" {
		t.Errorf("label first: %s", got)
	}
	if got := owner(cart); got != "team-shop/backstage" {
		t.Errorf("by annotation: %s", got)
	}
	if got := owner(pay); got != "team-payments/backstage" {
		t.Errorf("by kubernetes-id: %s", got)
	}
}

func TestBackstageTeam(t *testing.T) {
	for ref, want := range map[string]string{
		"group:default/team-a": "team-a", "group:team-a": "team-a", "team-a": "team-a", "Group:ops/sre": "sre",
		"user:default/alice": "", "": "",
	} {
		if got := backstageTeam(ref); got != want {
			t.Errorf("%q: %q, want %q", ref, got, want)
		}
	}
}
