// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package ui

import (
	"errors"
	"net/http"
	"strings"

	"github.com/pipozzz/goliash/internal/auth"
	"github.com/pipozzz/goliash/internal/store"
)

// SSOView is the single sign-on page: the OpenID Connect provider, and the roles its groups give.
type SSOView struct {
	Base
	FromEnv     bool // set up by GOLIASH_OIDC_*: shown, not changed here
	Enabled     bool // sign-in with the provider works now
	Saved       bool // settings are saved in Goliash
	HasSecret   bool
	Settings    store.SSOSettings // the client secret left out
	Rows        []SSOGroupRow
	Workspaces  []store.Workspace
	CallbackURL string
	Test        *auth.SSOTest
	UpdatedBy   string
}

// SSOGroupRow is one group's role on the page.
type SSOGroupRow struct {
	Group, Role, Workspace string
}

// ssoRows are the group roles as rows, with empty ones to add more.
func ssoRows(g auth.GroupRoles) []SSOGroupRow {
	rows := make([]SSOGroupRow, 0, len(g)+3)
	for _, e := range g {
		rows = append(rows, SSOGroupRow{Group: e.Group, Role: e.Role, Workspace: e.Workspace})
	}
	for range 3 {
		rows = append(rows, SSOGroupRow{Role: store.RoleMember})
	}
	return rows
}

// ssoOwner lets only owners change how people sign in: the provider's groups can make anyone an admin.
func (s *Server) ssoOwner(w http.ResponseWriter, r *http.Request, p auth.Principal) bool {
	if p.User.Role != store.RoleOwner {
		s.problem(w, r, http.StatusForbidden, "Not allowed", "Only owners set up single sign-on.", true)
		return false
	}
	return true
}

func (s *Server) ssoPage(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	if !s.ssoOwner(w, r, p) {
		return nil
	}
	return s.renderSSO(w, r, p, store.SSOSettings{}, nil, "")
}

// renderSSO shows the page; form, when set, holds what was typed (shown again after a mistake).
func (s *Server) renderSSO(w http.ResponseWriter, r *http.Request, p auth.Principal, form store.SSOSettings, rows []SSOGroupRow, problem string) error {
	ctx := r.Context()
	v := SSOView{
		Base: withFlash(s.base(ctx, p, "settings", "Single sign-on"), r), FromEnv: s.auth.OIDCFromEnv(), Enabled: s.auth.OIDCEnabled(),
		CallbackURL: strings.TrimSuffix(s.publicURL, "/") + "/auth/oidc/callback",
	}
	if problem != "" {
		v.Error = problem
	}
	saved, err := s.store.GetSSO(ctx, p.User.OrgID)
	switch {
	case err == nil:
		v.Saved, v.HasSecret, v.UpdatedBy = true, saved.ClientSecret != "", saved.UpdatedBy
		saved.ClientSecret = ""
	case errors.Is(err, store.ErrNotFound):
		saved = store.SSOSettings{GroupsClaim: "groups"}
	default:
		return err
	}
	v.Settings = saved
	if form.Issuer != "" || rows != nil {
		v.Settings = form
		v.Settings.ClientSecret = ""
	}
	if rows != nil {
		v.Rows = rows
	} else {
		groups, _ := auth.ParseGroupRoles(v.Settings.GroupRoles)
		v.Rows = ssoRows(groups)
	}
	if v.Workspaces, err = s.store.ListOrgWorkspaces(ctx, p.User.OrgID); err != nil {
		return err
	}
	if key := r.URL.Query().Get("test"); key != "" {
		if t, ok := s.auth.SSOTestResult(key); ok {
			v.Test = &t
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	return render(w, r, SSOPage(v))
}

// ssoSave checks the provider (it must answer) and saves the settings; the sign-in changes at once.
func (s *Server) ssoSave(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	if !s.ssoOwner(w, r, p) {
		return nil
	}
	if s.auth.OIDCFromEnv() {
		return back(w, r, "/settings/sso", "error", "Single sign-on is set up by the environment (GOLIASH_OIDC_*); change it there.")
	}
	if err := r.ParseForm(); err != nil {
		return err
	}
	ctx := r.Context()
	f := r.PostForm
	val := func(k string) string { return strings.TrimSpace(f.Get(k)) }
	v := store.SSOSettings{
		OrgID: p.User.OrgID, Issuer: val("issuer"), ClientID: val("client_id"), ClientSecret: val("client_secret"), Name: val("name"),
		Domains: val("domains"), GroupsClaim: val("groups_claim"), Scopes: val("scopes"), UpdatedBy: p.User.Email,
	}
	var rows []SSOGroupRow
	var entries []string
	groups, roles, wss := f["group"], f["role"], f["workspace"]
	for i, g := range groups {
		row := SSOGroupRow{Group: strings.TrimSpace(g)}
		if i < len(roles) {
			row.Role = roles[i]
		}
		if i < len(wss) {
			row.Workspace = wss[i]
		}
		rows = append(rows, row)
		if row.Group == "" {
			continue
		}
		entry := row.Group + "=" + row.Role
		if row.Workspace != "" {
			entry += "@" + row.Workspace
		}
		entries = append(entries, entry)
	}
	v.GroupRoles = strings.Join(entries, ", ")
	again := func(problem string) error { return s.renderSSO(w, r, p, v, rows, problem) }
	if !strings.HasPrefix(v.Issuer, "https://") && !strings.HasPrefix(v.Issuer, "http://localhost") {
		return again("The issuer is an https:// URL, e.g. https://accounts.google.com.")
	}
	if v.ClientID == "" {
		return again("Enter the client ID from the provider.")
	}
	if v.ClientSecret == "" {
		if old, err := s.store.GetSSO(ctx, p.User.OrgID); err == nil && old.ClientSecret != "" {
			v.ClientSecret = old.ClientSecret // left blank: keep the saved one
		} else {
			return again("Enter the client secret from the provider.")
		}
	}
	if _, err := auth.ParseGroupRoles(v.GroupRoles); err != nil {
		return again(err.Error() + ". Admin is for the whole organization: leave its workspace empty.")
	}
	if !s.store.Encrypts() {
		return again("Not saved: " + store.ErrSSOUnencrypted.Error() + ".")
	}
	if err := s.auth.ApplySSO(ctx, v); err != nil {
		return again("The provider did not answer as expected, so nothing changed: " + err.Error())
	}
	if err := s.store.SetSSO(ctx, v); err != nil {
		return err
	}
	s.audit(ctx, p, "sso.save", "issuer", v.Issuer, "group_roles", v.GroupRoles)
	return back(w, r, "/settings/sso", "notice", "Saved: the sign-in page offers "+s.auth.OIDCName()+" now. Test it below before you rely on it.")
}

// ssoDelete turns single sign-on set up in Goliash off. Accounts stay; people sign in with a link or a password.
func (s *Server) ssoDelete(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	if !s.ssoOwner(w, r, p) {
		return nil
	}
	if s.auth.OIDCFromEnv() {
		return back(w, r, "/settings/sso", "error", "Single sign-on is set up by the environment (GOLIASH_OIDC_*); change it there.")
	}
	if err := s.store.DeleteSSO(r.Context(), p.User.OrgID); err != nil {
		return err
	}
	s.auth.SetOIDC(nil)
	s.audit(r.Context(), p, "sso.delete")
	return back(w, r, "/settings/sso", "notice", "Single sign-on is off. People sign in with a link or a password.")
}
