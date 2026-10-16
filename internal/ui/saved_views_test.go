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

func TestViewURL(t *testing.T) {
	for raw, want := range map[string]string{
		"/tiles?group=team&app=payments&tv=1":   "/tiles?app=payments&group=team&tv=1",
		"/tiles?app=payments&notice=Saved&at=x": "/tiles?app=payments",
		"/":                                     "/?view=table",
		"/?q=post&drift=1":                      "/?drift=1&q=post&view=table",
		"/updates?team=payments&env=prod":       "/updates?env=prod&team=payments",
		"/events":                               "",
		"//evil.example/tiles":                  "",
		"https://evil.example/tiles":            "",
		"/tiles?app=a%26b":                      "/tiles?app=a%26b",
		"":                                      "",
	} {
		if got := viewURL(raw); got != want {
			t.Errorf("viewURL(%q) = %q, want %q", raw, got, want)
		}
	}
}

// Saved views: a person's own and the workspace's, as tabs; one can be the home; members manage shared ones.
func TestSavedViews(t *testing.T) {
	e := newUIEnv(t)
	ctx := context.Background()
	sign := func(email, role string) (*http.Client, store.User) {
		u, _ := e.st.CreateUser(ctx, e.ws.OrgID, email, "", role)
		_ = e.st.SetMembership(ctx, u.ID, e.ws.ID, role)
		link, _ := e.auth.LoginLink(ctx, u)
		jar, _ := cookiejar.New(nil)
		c := &http.Client{Jar: jar}
		get(t, c, link, nil)
		return c, u
	}
	pat, patUser := sign("pat@example.com", store.RoleMember)
	vic, _ := sign("vic@example.com", store.RoleViewer)

	// Pat saves the payments team's board for everyone, and their own TV board.
	if _, body, _ := post(t, pat, e.srv.URL+"/views", url.Values{"name": {"payments"}, "url": {"/tiles?group=team&app=payments"}, "shared": {"1"}}); !strings.Contains(body, "Saved as payments") {
		t.Fatalf("save shared: %s", body)
	}
	if _, body, _ := post(t, pat, e.srv.URL+"/views", url.Values{"name": {"wall"}, "url": {"/tiles?tv=1&env=prod"}}); !strings.Contains(body, "Saved as wall") {
		t.Fatalf("save own: %s", body)
	}
	if _, body, _ := post(t, pat, e.srv.URL+"/views", url.Values{"name": {"wall"}, "url": {"/tiles?env=dev"}}); !strings.Contains(body, "named wall already") {
		t.Fatalf("same name: %s", body)
	}
	if _, body, _ := post(t, pat, e.srv.URL+"/views", url.Values{"name": {"x"}, "url": {"https://evil.example/"}}); !strings.Contains(body, "Only the matrix, the tiles and Updates") {
		t.Fatalf("an outside address: %s", body)
	}
	// Updates have their own views: a team's upgrade list, a tab there and not above the tiles.
	if _, body, _ := post(t, pat, e.srv.URL+"/views", url.Values{"name": {"payments upgrades"}, "url": {"/updates?team=payments"}}); !strings.Contains(body, "a tab above Updates now") {
		t.Fatalf("save updates view: %s", body)
	}
	if _, page := get(t, pat, e.srv.URL+"/updates", nil); !strings.Contains(page, ">payments upgrades") || strings.Contains(page, ">wall") {
		t.Fatalf("updates tabs: %s", page)
	}
	if _, page := get(t, pat, e.srv.URL+"/tiles", nil); strings.Contains(page, ">payments upgrades") {
		t.Fatal("an Updates view is a tab above the tiles")
	}
	views0, _ := e.st.ListSavedViews(ctx, e.ws.Scope(), patUser.ID)
	for _, sv := range views0 {
		if sv.Name == "payments upgrades" {
			_, _, _ = post(t, pat, e.srv.URL+"/views/"+sv.ID+"/delete", nil)
		}
	}
	// Vic, a viewer, sees the shared one but not Pat's, may keep their own, and may not share.
	_, page := get(t, vic, e.srv.URL+"/tiles", nil)
	if !strings.Contains(page, ">payments") || strings.Contains(page, ">wall") || !strings.Contains(page, "+ Save view") {
		t.Fatalf("viewer's tabs: %s", page)
	}
	if strings.Contains(page, `name="shared"`) {
		t.Error("a viewer is offered to share")
	}
	if _, body, _ := post(t, vic, e.srv.URL+"/views", url.Values{"name": {"mine"}, "url": {"/tiles?env=prod"}, "shared": {"1"}}); !strings.Contains(body, "Only members save views for everyone") {
		t.Fatalf("viewer shared: %s", body)
	}
	// The active tab offers home and delete; a viewer cannot delete a shared view.
	views, _ := e.st.ListSavedViews(ctx, e.ws.Scope(), patUser.ID)
	if len(views) != 2 || views[0].Name != "payments" || views[0].UserID != "" || views[1].UserID != patUser.ID {
		t.Fatalf("views %+v", views)
	}
	shared, own := views[0], views[1]
	if _, page = get(t, vic, e.srv.URL+shared.URL, nil); !strings.Contains(page, `aria-current="page">payments`) && !strings.Contains(page, "view-tab on") {
		t.Fatalf("active tab: %s", page)
	}
	if strings.Contains(page, "/views/"+shared.ID+"/delete") {
		t.Error("a viewer may delete a shared view")
	}
	if _, body, _ := post(t, vic, e.srv.URL+"/views/"+shared.ID+"/delete", nil); !strings.Contains(body, "not yours to delete") {
		t.Fatalf("viewer deleted: %s", body)
	}
	// Pat makes the TV board home: "/" opens it, and switching to the table keeps it.
	if _, body, _ := post(t, pat, e.srv.URL+"/views/"+own.ID+"/home", nil); !strings.Contains(body, "Goliash opens with wall now") {
		t.Fatalf("home: %s", body)
	}
	noRedirect := *pat
	noRedirect.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := noRedirect.Get(e.srv.URL + "/") //nolint:noctx // test
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusFound || resp.Header.Get("Location") != own.URL {
		t.Fatalf("/ went %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}
	get(t, pat, e.srv.URL+"/?view=table", nil)
	if got, _ := e.st.GetUser(ctx, patUser.ID); got.HomeView != "view:"+own.ID {
		t.Fatalf("the table took over the home: %q", got.HomeView)
	}
	// Deleting it ends it being home; the account page offers views as homes.
	if _, page = get(t, pat, e.srv.URL+"/account", nil); !strings.Contains(page, `value="view:`+shared.ID+`"`) {
		t.Fatalf("account page misses the views: %s", page)
	}
	if _, body, _ := post(t, pat, e.srv.URL+"/views/"+own.ID+"/delete", nil); !strings.Contains(body, "wall is deleted") {
		t.Fatalf("delete: %s", body)
	}
	if got, _ := e.st.GetUser(ctx, patUser.ID); strings.HasPrefix(got.HomeView, "view:") {
		t.Fatalf("home kept on a deleted view: %q", got.HomeView)
	}
}
