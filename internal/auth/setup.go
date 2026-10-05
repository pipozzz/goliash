// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package auth

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/pipozzz/goliash/internal/store"
)

// setupTTL is how long the first-account link works; a restart logs a new one.
const setupTTL = 24 * time.Hour

// SetupLink creates the one-time link that creates the first owner in the browser.
// The server logs it while nobody has an account: only someone who can read the
// server's logs can use it.
func (a *Auth) SetupLink(ctx context.Context) (string, error) {
	raw, hash := randomToken()
	if err := a.store.CreateSetupToken(ctx, hash, setupTTL); err != nil {
		return "", err
	}
	return a.publicURL.String() + "/setup?token=" + url.QueryEscape(raw), nil
}

// SetupTokenValid reports whether a setup link still works.
func (a *Auth) SetupTokenValid(ctx context.Context, raw string) bool {
	org, err := a.orgID(ctx)
	if err != nil || raw == "" {
		return false
	}
	ok, err := a.store.SetupTokenValid(ctx, org, hashOf(raw))
	return err == nil && ok
}

func (a *Auth) orgID(ctx context.Context) (string, error) {
	ws, err := a.store.ListWorkspaces(ctx)
	if err != nil || len(ws) == 0 {
		return "", errors.New("no workspace")
	}
	return ws[0].OrgID, nil
}

// setup creates the first owner from the setup page and signs them in.
func (a *Auth) setup(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	token := r.FormValue("token")
	again := func(problem string) {
		http.Redirect(w, r, "/setup?token="+url.QueryEscape(token)+"&error="+url.QueryEscape(problem), http.StatusSeeOther) //nolint:gosec // same-site path
	}
	email := strings.TrimSpace(r.FormValue("email"))
	if !strings.Contains(email, "@") {
		again("Enter your e-mail address.")
		return
	}
	password := r.FormValue("password")
	if password != r.FormValue("confirm") {
		again("The two passwords differ.")
		return
	}
	if err := CheckPassword(password, email); err != nil {
		again("Choose another password: " + strings.TrimPrefix(err.Error(), ErrWeakPassword.Error()+": ") + ".")
		return
	}
	org, err := a.orgID(ctx)
	if err != nil {
		http.Error(w, "could not set up", http.StatusInternalServerError)
		return
	}
	u, err := a.store.CreateFirstOwner(ctx, org, hashOf(token), email, strings.TrimSpace(r.FormValue("name")), HashPassword(password))
	if errors.Is(err, store.ErrNotFound) {
		http.Redirect(w, r, "/login?error=setup", http.StatusSeeOther)
		return
	}
	if err != nil {
		http.Error(w, "could not set up", http.StatusInternalServerError)
		return
	}
	if err := a.store.Audit(ctx, store.AuditEntry{OrgID: org, Actor: u.Email, Action: "user.create", Details: map[string]string{"role": "owner", "via": "setup link"}}); err != nil {
		a.log.ErrorContext(ctx, "audit log write failed", "err", err)
	}
	a.log.InfoContext(ctx, "first owner created with the setup link", "user", u.Email)
	if err := a.startSession(w, r, u, "password"); err != nil {
		http.Error(w, "could not sign in", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// NoAccounts reports whether nobody can sign in yet (the login page then points to
// the setup link in the server log).
func (a *Auth) NoAccounts(ctx context.Context) bool {
	org, err := a.orgID(ctx)
	if err != nil {
		return false
	}
	n, err := a.store.CountUsers(ctx, org)
	return err == nil && n == 0
}
