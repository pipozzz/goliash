// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package ui

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/pipozzz/goliash/internal/store"
)

// With a Jira channel, a member files a ticket for an upgrade from Updates; then everyone sees its key there.
func TestCreateTicketFromUpdates(t *testing.T) {
	filed := 0
	jira := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/rest/api/2/issue" {
			filed++
			_, _ = w.Write([]byte(`{"id":"9","key":"OPS-9"}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer jira.Close()
	e := newUIEnv(t)
	ctx := context.Background()
	sc := e.ws.Scope()
	envs, _ := e.st.ListEnvironments(ctx, sc)
	prod := envs[len(envs)-1]
	pg, _ := e.st.EnsureService(ctx, sc, "postgres")
	if _, err := e.st.OpenDrift(ctx, store.Drift{
		Scope: sc, ServiceID: pg.ID, EnvironmentID: prod.ID, Kind: "upstream",
		Detail: json.RawMessage(`{"running":"15.6","other":"16.4","jump":"major"}`),
	}); err != nil && !errors.Is(err, store.ErrExists) {
		t.Fatal(err)
	}
	cfg, _ := json.Marshal(map[string]string{"url": jira.URL, "token": "t", "project": "OPS"})
	ch, err := e.st.CreateChannel(ctx, store.Channel{Scope: sc, Type: "jira", Name: "jira", Config: cfg})
	if err != nil {
		t.Fatal(err)
	}
	viewer := e.as(store.RoleViewer)
	if _, body := get(t, viewer, e.srv.URL+"/updates", nil); strings.Contains(body, "Create ticket") {
		t.Error("a viewer can file tickets")
	}
	member := e.as(store.RoleMember)
	if _, body := get(t, member, e.srv.URL+"/updates", nil); !strings.Contains(body, ">Create ticket<") {
		t.Fatal("no Create ticket for a member")
	}
	_, body, _ := post(t, member, e.srv.URL+"/updates/ticket", url.Values{
		"service": {pg.ID}, "env": {prod.ID}, "channel": {ch.ID}, "return": {"/updates"},
	})
	if filed != 1 || !strings.Contains(body, "Ticket OPS-9 filed") {
		t.Fatalf("filed %d: %s", filed, body[:min(len(body), 400)])
	}
	_, body = get(t, viewer, e.srv.URL+"/updates", nil)
	if !strings.Contains(body, ">OPS-9<") || !strings.Contains(body, jira.URL+"/browse/OPS-9") {
		t.Error("the ticket is not shown")
	}
	if _, page := get(t, viewer, e.srv.URL+"/services/postgres", nil); !strings.Contains(page, ">OPS-9<") {
		t.Error("the service page does not show the ticket")
	}
}
