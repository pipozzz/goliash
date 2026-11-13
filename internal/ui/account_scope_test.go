// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package ui

import (
	"context"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"testing"

	"github.com/pipozzz/goliash/internal/store"
)

// A workspace's admin manages the accounts of people who use that workspace alone: not someone who also uses
// another, nor anyone who already has an account, by adding them.
func TestWorkspaceAdminAccountScope(t *testing.T) {
	e := newUIEnv(t)
	ctx := context.Background()
	other, err := e.st.CreateWorkspace(ctx, e.ws.OrgID, "Other", "other")
	if err != nil {
		t.Fatal(err)
	}
	admin, _ := e.st.CreateUser(ctx, e.ws.OrgID, "wsadmin@example.com", "", store.RoleMember)
	_ = e.st.SetMembership(ctx, admin.ID, e.ws.ID, store.RoleAdmin)
	both, _ := e.st.CreateUser(ctx, e.ws.OrgID, "both@example.com", "", store.RoleMember)
	_ = e.st.SetMembership(ctx, both.ID, e.ws.ID, store.RoleViewer)
	_ = e.st.SetMembership(ctx, both.ID, other.ID, store.RoleAdmin)
	only, _ := e.st.CreateUser(ctx, e.ws.OrgID, "only@example.com", "", store.RoleMember)
	_ = e.st.SetMembership(ctx, only.ID, e.ws.ID, store.RoleViewer)
	elsewhere, _ := e.st.CreateUser(ctx, e.ws.OrgID, "elsewhere@example.com", "", store.RoleMember)
	_ = e.st.SetMembership(ctx, elsewhere.ID, other.ID, store.RoleAdmin)

	link, _ := e.auth.LoginLink(ctx, admin)
	jar, _ := cookiejar.New(nil)
	c := &http.Client{Jar: jar}
	if code := signIn(t, c, link); code != 200 {
		t.Fatalf("sign-in %d", code)
	}
	signInLink := func(u store.User) bool {
		_, body, _ := post(t, c, e.srv.URL+"/settings/users/"+u.ID+"/link", url.Values{})
		return strings.Contains(body, "Sign-in link for "+u.Email)
	}
	if signInLink(both) {
		t.Error("a workspace admin got a sign-in link for someone who also uses another workspace")
	}
	if !signInLink(only) {
		t.Error("a workspace admin got no sign-in link for someone of their workspace alone")
	}
	_, _, _ = post(t, c, e.srv.URL+"/settings/users", url.Values{"email": {elsewhere.Email}, "role": {store.RoleViewer}})
	if roles, _ := e.st.WorkspaceRoles(ctx, e.ws.ID); roles[elsewhere.ID] != "" {
		t.Error("a workspace admin added someone who already has an account")
	}
}
