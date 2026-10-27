// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package scim

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/pipozzz/goliash/internal/auth"
	"github.com/pipozzz/goliash/internal/store"
	"github.com/pipozzz/goliash/internal/store/storetest"
)

const token = "0123456789abcdef0123456789abcdef"

type env struct {
	t   *testing.T
	st  *store.Store
	ws  store.Workspace
	pay store.Workspace
	srv *httptest.Server
}

func newEnv(t *testing.T, roles string) *env {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, storetest.DSN(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ws, _ := st.EnsureDefaultWorkspace(ctx)
	pay, _ := st.CreateWorkspace(ctx, ws.OrgID, "Payments", "payments")
	gr, err := auth.ParseGroupRoles(roles)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(New(st, token, gr, "https://goliash.example", slog.New(slog.NewTextHandler(io.Discard, nil))))
	t.Cleanup(srv.Close)
	return &env{t: t, st: st, ws: ws, pay: pay, srv: srv}
}

func (e *env) do(method, path, body string) (int, map[string]any) {
	e.t.Helper()
	req, _ := http.NewRequestWithContext(context.Background(), method, e.srv.URL+path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/scim+json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func (e *env) access(email string) (string, map[string]string, bool) {
	e.t.Helper()
	u, err := e.st.GetUserByEmail(context.Background(), e.ws.OrgID, email)
	if err != nil {
		e.t.Fatal(err)
	}
	acc, _ := e.st.UserWorkspaces(context.Background(), u)
	ws := map[string]string{}
	for _, a := range acc {
		ws[a.Workspace.Slug] = a.Role
	}
	return u.Role, ws, u.Disabled
}

func TestProvisioning(t *testing.T) {
	e := newEnv(t, "goliash-admins=admin, sre-pay=member@payments, everyone=viewer")
	ctx := context.Background()

	// The provider looks the person up, finds nothing, and creates them.
	code, list := e.do("GET", `/scim/v2/Users?filter=userName+eq+"dev@acme.io"`, "")
	if code != 200 || list["totalResults"].(float64) != 0 {
		t.Fatalf("lookup: %d %v", code, list)
	}
	code, user := e.do("POST", "/scim/v2/Users", `{"schemas":["urn:ietf:params:scim:schemas:core:2.0:User"],"userName":"Dev@acme.io",
		"externalId":"00u1","name":{"givenName":"Dana","familyName":"Dev"},"emails":[{"value":"dev@acme.io","primary":true}],"active":true}`)
	if code != 201 || user["userName"] != "dev@acme.io" || user["externalId"] != "00u1" {
		t.Fatalf("create: %d %v", code, user)
	}
	id := user["id"].(string)
	if code, _ := e.do("POST", "/scim/v2/Users", `{"userName":"dev@acme.io"}`); code != 409 {
		t.Errorf("duplicate: %d", code)
	}
	// In no group yet: an account with no workspace.
	if org, ws, _ := e.access("dev@acme.io"); org != store.RoleViewer || len(ws) != 0 {
		t.Errorf("before groups: %s %v", org, ws)
	}

	// Groups pushed: everyone (viewer everywhere) and sre-pay (member in payments).
	_, everyone := e.do("POST", "/scim/v2/Groups", `{"displayName":"everyone","members":[{"value":"`+id+`"}]}`)
	code, srePay := e.do("POST", "/scim/v2/Groups", `{"displayName":"sre-pay","externalId":"g-2"}`)
	if code != 201 {
		t.Fatalf("group: %d %v", code, srePay)
	}
	if code, _ := e.do("PATCH", "/scim/v2/Groups/"+srePay["id"].(string), `{"schemas":["urn:ietf:params:scim:api:messages:2.0:PatchOp"],
		"Operations":[{"op":"add","path":"members","value":[{"value":"`+id+`"}]}]}`); code != 200 {
		t.Fatalf("add member: %d", code)
	}
	if org, ws, _ := e.access("dev@acme.io"); org != store.RoleMember || ws["payments"] != store.RoleMember || ws[e.ws.Slug] != store.RoleViewer {
		t.Errorf("with groups: %s %v", org, ws)
	}
	// Removed from sre-pay the Entra way: members[value eq "id"].
	e.do("PATCH", "/scim/v2/Groups/"+srePay["id"].(string), `{"Operations":[{"op":"remove","path":"members[value eq \"`+id+`\"]"}]}`)
	if _, ws, _ := e.access("dev@acme.io"); ws["payments"] != store.RoleViewer {
		t.Errorf("after leaving sre-pay: %v", ws)
	}
	_, got := e.do("GET", "/scim/v2/Users/"+id, "")
	if gs, _ := got["groups"].([]any); len(gs) != 1 {
		t.Errorf("groups on the user: %v", got["groups"])
	}

	// They leave: Entra deactivates with a string. Sessions end, sign-in is refused.
	u, _ := e.st.GetUser(ctx, id)
	_ = e.st.CreateSession(ctx, "h1", u.ID, time.Hour, store.Session{})
	code, _ = e.do("PATCH", "/scim/v2/Users/"+id, `{"Operations":[{"op":"Replace","path":"active","value":"False"}]}`)
	if _, _, disabled := e.access("dev@acme.io"); code != 200 || !disabled {
		t.Fatalf("deactivate: %d %v", code, disabled)
	}
	if _, _, err := e.st.SessionUser(ctx, "h1"); err == nil {
		t.Error("the session outlived deactivation")
	}
	// Okta's way back: replace with an object.
	e.do("PATCH", "/scim/v2/Users/"+id, `{"Operations":[{"op":"replace","value":{"active":true}}]}`)
	if _, _, disabled := e.access("dev@acme.io"); disabled {
		t.Error("not active again")
	}

	// Deleting the group takes its role away; deleting the user removes them.
	e.do("DELETE", "/scim/v2/Groups/"+everyone["id"].(string), "")
	if _, ws, _ := e.access("dev@acme.io"); len(ws) != 0 {
		t.Errorf("after the group went: %v", ws)
	}
	if code, _ := e.do("DELETE", "/scim/v2/Users/"+id, ""); code != 204 {
		t.Errorf("delete: %d", code)
	}
	if code, _ := e.do("GET", "/scim/v2/Users/"+id, ""); code != 404 {
		t.Errorf("deleted user: %d", code)
	}
}

func TestOwnersAndToken(t *testing.T) {
	e := newEnv(t, "")
	ctx := context.Background()
	owner, _ := e.st.CreateUser(ctx, e.ws.OrgID, "owner@acme.io", "", store.RoleOwner)
	if code, _ := e.do("PATCH", "/scim/v2/Users/"+owner.ID, `{"Operations":[{"op":"replace","path":"active","value":false}]}`); code != 409 {
		t.Errorf("deactivating the last owner: %d", code)
	}
	if code, _ := e.do("DELETE", "/scim/v2/Users/"+owner.ID, ""); code != 409 {
		t.Errorf("deleting the last owner: %d", code)
	}
	// Without group roles a new person is a viewer of the first workspace.
	e.do("POST", "/scim/v2/Users", `{"userName":"new@acme.io"}`)
	if org, ws, _ := e.access("new@acme.io"); org != store.RoleViewer || ws[e.ws.Slug] != store.RoleViewer || len(ws) != 1 {
		t.Errorf("new without roles: %s %v", org, ws)
	}
	req, _ := http.NewRequestWithContext(ctx, "GET", e.srv.URL+"/scim/v2/Users", nil)
	req.Header.Set("Authorization", "Bearer wrong")
	resp, _ := http.DefaultClient.Do(req)
	if resp.StatusCode != 401 {
		t.Errorf("wrong token: %d", resp.StatusCode)
	}
	_ = resp.Body.Close()
	if code, cfg := e.do("GET", "/scim/v2/ServiceProviderConfig", ""); code != 200 || cfg["patch"] == nil {
		t.Errorf("service provider config: %d", code)
	}
}
