// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package auth

import (
	"context"
	"testing"

	"github.com/pipozzz/goliash/internal/store"
)

func TestParseGroupRoles(t *testing.T) {
	g, err := ParseGroupRoles(" goliash-admins=admin, sre=member, sre-pay=member@payments,everyone = viewer ")
	if err != nil || len(g) != 4 || g[2] != (GroupRole{"sre-pay", "member", "payments"}) {
		t.Fatalf("%+v %v", g, err)
	}
	for _, bad := range []string{"sre", "sre=owner", "x=admin@payments", "=viewer"} {
		if _, err := ParseGroupRoles(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	got, ok := g.For([]string{"/sre-pay", "everyone"})
	if !ok || got.OrgRole != store.RoleMember || got.Workspaces["payments"] != store.RoleMember || got.Workspaces[""] != store.RoleViewer {
		t.Errorf("grants %+v", got)
	}
	if _, ok := g.For([]string{"marketing"}); ok {
		t.Error("a group with no role gave one")
	}
}

// With group roles, the provider's groups decide who signs in and with which role, at every sign-in.
func TestOIDCGroupRoles(t *testing.T) {
	h := newHarness(t, false)
	ctx := context.Background()
	p := newFakeProvider(t)
	_, _ = h.st.CreateWorkspace(ctx, h.ws.OrgID, "Payments", "payments")
	groups, _ := ParseGroupRoles("goliash-admins=admin, sre-pay=member@payments, everyone=viewer")
	o, err := NewOIDC(ctx, OIDCConfig{Issuer: p.srv.URL, ClientID: "goliash", ClientSecret: "s", Groups: groups}, h.srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	h.auth.SetOIDC(o)
	_, _ = h.st.CreateUser(ctx, h.ws.OrgID, "owner@acme.io", "", store.RoleOwner)

	signIn := func(email string, groups ...string) string {
		t.Helper()
		p.mu.Lock()
		p.email, p.verify, p.groups = email, true, groups
		p.mu.Unlock()
		cl := h.client()
		_, body := get(t, cl, h.srv.URL+"/auth/oidc/start")
		if body != "home" {
			return "refused"
		}
		_, who := get(t, cl, h.srv.URL+"/whoami")
		return who
	}
	roles := func(email string) (string, map[string]string) {
		t.Helper()
		u, err := h.st.GetUserByEmail(ctx, h.ws.OrgID, email)
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]string{}
		acc, _ := h.st.UserWorkspaces(ctx, u)
		for _, a := range acc {
			out[a.Workspace.Slug] = a.Role
		}
		return u.Role, out
	}

	// A new person in sre-pay and everyone: created without an invitation, member in payments, viewer elsewhere.
	if got := signIn("dev@acme.io", "sre-pay", "everyone"); got == "refused" {
		t.Fatal("dev refused")
	}
	if org, ws := roles("dev@acme.io"); org != store.RoleMember || ws["payments"] != store.RoleMember || ws[h.ws.Slug] != store.RoleViewer {
		t.Fatalf("dev: %s %v", org, ws)
	}
	// Leaving sre-pay takes payments away at the next sign-in; joining the admins makes them an admin.
	signIn("dev@acme.io", "everyone")
	if _, ws := roles("dev@acme.io"); ws["payments"] != store.RoleViewer {
		t.Errorf("after leaving sre-pay: %v", ws)
	}
	signIn("dev@acme.io", "goliash-admins")
	if org, _ := roles("dev@acme.io"); org != store.RoleAdmin {
		t.Errorf("after joining the admins: %s", org)
	}
	// In no group: refused, also for an existing account; the owner stays the owner.
	if got := signIn("dev@acme.io", "marketing"); got != "refused" {
		t.Errorf("no group: %s", got)
	}
	if got := signIn("stranger@acme.io"); got != "refused" {
		t.Errorf("stranger: %s", got)
	}
	signIn("owner@acme.io", "everyone")
	if org, _ := roles("owner@acme.io"); org != store.RoleOwner {
		t.Errorf("owner became %s", org)
	}
}
