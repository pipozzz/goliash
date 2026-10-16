// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package ui

import (
	"context"
	"net/http"
	"net/url"
	"sort"
	"strings"

	"github.com/pipozzz/goliash/internal/auth"
	"github.com/pipozzz/goliash/internal/store"
)

// ViewTab is a saved view as a tab above the matrix and the tiles.
type ViewTab struct {
	ID, Name, URL string
	Shared        bool // everyone in the workspace sees it
	CanDelete     bool
	Active        bool // the page shows this view now
	Home          bool // the person's home
}

// ViewTabs are the saved views of a page, and what saving the page as a view needs.
type ViewTabs struct {
	Tabs      []ViewTab
	AllURL    string // the page without a view's filters
	Current   string // the page's address, as a view would keep it
	CanShare  bool   // may save views for the whole workspace
	CanSave   bool
	AnyActive bool
}

const (
	maxViewName  = 60
	maxOwnViews  = 40
	homeViewPref = "view:"
)

// viewURL is an address as a saved view keeps it: the matrix or the tiles, with their filters in a stable order,
// without one-off parameters (a flash message, a point in time). Empty for anything else.
func viewURL(raw string) string {
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.IsAbs() || u.Host != "" || strings.HasPrefix(raw, "//") {
		return ""
	}
	q := u.Query()
	for _, k := range []string{"notice", "error", "at"} {
		q.Del(k)
	}
	switch u.Path {
	case "/", "":
		q.Set("view", "table") // "/" alone opens the home, which may be this very view
		u.Path = "/"
	case "/tiles", "/updates":
	default:
		return ""
	}
	keys := make([]string, 0, len(q))
	for k := range q {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for i, k := range keys {
		if i > 0 {
			b.WriteByte('&')
		}
		b.WriteString(url.QueryEscape(k) + "=" + url.QueryEscape(q.Get(k)))
	}
	return u.Path + "?" + b.String()
}

// viewPage is the kind of page a view shows: the board (the matrix and the tiles, one picture of what runs) or
// Updates. A page's tabs are the views of its kind.
func viewPage(u string) string {
	if strings.HasPrefix(u, "/updates") {
		return "updates"
	}
	return "board"
}

// viewTabs loads the saved views for a matrix, tiles or Updates page: those of its kind.
func (s *Server) viewTabs(ctx context.Context, r *http.Request, p auth.Principal, allURL string) ViewTabs {
	t := ViewTabs{AllURL: allURL, Current: viewURL(r.URL.RequestURI()), CanShare: p.Can(store.RoleMember), CanSave: !s.auth.Demo() && p.User.ID != ""}
	views, err := s.store.ListSavedViews(ctx, p.Scope, p.User.ID)
	if err != nil {
		return t
	}
	for _, v := range views {
		if viewPage(v.URL) != viewPage(allURL) {
			continue
		}
		tab := ViewTab{
			ID: v.ID, Name: v.Name, URL: v.URL, Shared: v.UserID == "",
			CanDelete: v.UserID == p.User.ID || (v.UserID == "" && p.Can(store.RoleMember)),
			Active:    v.URL == t.Current, Home: p.User.HomeView == homeViewPref+v.ID,
		}
		t.AnyActive = t.AnyActive || tab.Active
		t.Tabs = append(t.Tabs, tab)
	}
	return t
}

// homeViewURL is the address of the person's home when it is a saved view they can still see, else "".
func (s *Server) homeViewURL(ctx context.Context, p auth.Principal) string {
	id, ok := strings.CutPrefix(p.User.HomeView, homeViewPref)
	if !ok {
		return ""
	}
	v, err := s.store.GetSavedView(ctx, p.Scope, id)
	if err != nil || (v.UserID != "" && v.UserID != p.User.ID) {
		return ""
	}
	return v.URL
}

// saveView names the page someone looks at, for them or for everyone in the workspace.
func (s *Server) saveView(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	ctx := r.Context()
	u := viewURL(r.FormValue("url"))
	from := u
	if from == "" {
		from = "/"
	}
	name := strings.TrimSpace(r.FormValue("name"))
	switch {
	case s.auth.Demo() || p.User.ID == "":
		return back(w, r, from, "error", "Views are kept per person; the demo has no persons.")
	case u == "":
		return back(w, r, "/", "error", "Only the matrix, the tiles and Updates can be saved as a view.")
	case name == "" || len(name) > maxViewName:
		return back(w, r, from, "error", "Name the view (up to 60 characters).")
	}
	shared := r.FormValue("shared") == "1"
	if shared && !p.Can(store.RoleMember) {
		return back(w, r, from, "error", "Only members save views for everyone.")
	}
	views, err := s.store.ListSavedViews(ctx, p.Scope, p.User.ID)
	if err != nil {
		return err
	}
	own := 0
	for _, v := range views {
		if v.UserID == p.User.ID {
			own++
		}
		if v.Name == name && (v.UserID == "" || v.UserID == p.User.ID) {
			return back(w, r, from, "error", "There is a view named "+name+" already.")
		}
	}
	if own >= maxOwnViews {
		return back(w, r, from, "error", "That is 40 views already; delete one first.")
	}
	v := store.SavedView{Scope: p.Scope, Name: name, URL: u}
	if !shared {
		v.UserID = p.User.ID
	}
	if _, err := s.store.CreateSavedView(ctx, v); err != nil {
		return err
	}
	if shared {
		s.audit(ctx, p, "view.create", "view", name, "url", u)
	}
	where := "above the matrix and the tiles"
	if viewPage(u) == "updates" {
		where = "above Updates"
	}
	return back(w, r, u, "notice", "Saved as "+name+". It is a tab "+where+" now.")
}

// deleteView removes a view: one's own, or a shared one for members.
func (s *Server) deleteView(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	ctx := r.Context()
	v, err := s.store.GetSavedView(ctx, p.Scope, r.PathValue("id"))
	if err != nil || (v.UserID != p.User.ID && (v.UserID != "" || !p.Can(store.RoleMember))) {
		return back(w, r, "/", "error", "That view is gone, or not yours to delete.")
	}
	if err := s.store.DeleteSavedView(ctx, p.Scope, v.ID); err != nil {
		return err
	}
	if v.UserID == "" {
		s.audit(ctx, p, "view.delete", "view", v.Name)
	}
	if p.User.HomeView == homeViewPref+v.ID {
		_ = s.store.SetHomeView(ctx, p.User.ID, "")
	}
	all := "/?view=table"
	switch {
	case strings.HasPrefix(v.URL, "/tiles"):
		all = "/tiles"
	case strings.HasPrefix(v.URL, "/updates"):
		all = "/updates"
	}
	return back(w, r, all, "notice", v.Name+" is deleted.")
}

// homeOnView makes a view the person's home, or stops it being that.
func (s *Server) homeOnView(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	ctx := r.Context()
	v, err := s.store.GetSavedView(ctx, p.Scope, r.PathValue("id"))
	if err != nil || (v.UserID != "" && v.UserID != p.User.ID) || p.User.ID == "" || s.auth.Demo() {
		return back(w, r, "/", "error", "That view is gone.")
	}
	if p.User.HomeView == homeViewPref+v.ID {
		if err := s.store.SetHomeView(ctx, p.User.ID, ""); err != nil {
			return err
		}
		return back(w, r, v.URL, "notice", v.Name+" is not your home any more.")
	}
	if err := s.store.SetHomeView(ctx, p.User.ID, homeViewPref+v.ID); err != nil {
		return err
	}
	return back(w, r, v.URL, "notice", "Goliash opens with "+v.Name+" now.")
}
