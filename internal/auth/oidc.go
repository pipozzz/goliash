// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"

	"github.com/pipozzz/goliash/internal/store"
)

// OIDCConfig configures sign-in with an OpenID Connect provider (Google, GitHub via
// an OIDC bridge, Keycloak, Authentik, Entra ID, …).
type OIDCConfig struct {
	Issuer       string
	ClientID     string
	ClientSecret string
	Name         string   // button label, e.g. "Google"
	Domains      []string // e-mail domains whose people are created as viewers on first sign-in
	// Groups maps the provider's groups to roles; with it, the groups decide who signs in and with which role.
	Groups      GroupRoles
	GroupsClaim string   // the ID token claim with the groups; default "groups"
	Scopes      []string // more scopes to ask for, e.g. "groups" for providers that send groups only then
}

// OIDC is a configured provider.
type OIDC struct {
	name     string
	domains  []string
	groups   GroupRoles
	claim    string
	oauth    oauth2.Config
	verifier *oidc.IDTokenVerifier
}

// NewOIDC discovers the provider. redirectBase is the public URL of this server.
func NewOIDC(ctx context.Context, cfg OIDCConfig, redirectBase string) (*OIDC, error) {
	p, err := oidc.NewProvider(ctx, cfg.Issuer)
	if err != nil {
		return nil, fmt.Errorf("oidc discovery: %w", err)
	}
	name := cfg.Name
	if name == "" {
		name = "single sign-on"
	}
	claim := cfg.GroupsClaim
	if claim == "" {
		claim = "groups"
	}
	return &OIDC{
		name:    name,
		domains: cfg.Domains,
		groups:  cfg.Groups,
		claim:   claim,
		oauth: oauth2.Config{
			ClientID: cfg.ClientID, ClientSecret: cfg.ClientSecret, Endpoint: p.Endpoint(),
			RedirectURL: strings.TrimSuffix(redirectBase, "/") + "/auth/oidc/callback",
			Scopes:      append([]string{oidc.ScopeOpenID, "email", "profile"}, cfg.Scopes...),
		},
		verifier: p.Verifier(&oidc.Config{ClientID: cfg.ClientID}),
	}, nil
}

const oidcCookie = "goliash_oidc" // state|nonce|pkce verifier, for the duration of one sign-in

func (a *Auth) oidcStart(w http.ResponseWriter, r *http.Request) {
	if a.oidc == nil {
		http.NotFound(w, r)
		return
	}
	state, _ := randomToken()
	nonce, _ := randomToken()
	verifier := oauth2.GenerateVerifier()
	http.SetCookie(w, &http.Cookie{ //nolint:gosec // Secure follows the public URL scheme; plain http is for local use
		Name: oidcCookie, Value: state + "|" + nonce + "|" + verifier, Path: "/auth/oidc", HttpOnly: true,
		Secure: a.publicURL.Scheme == "https", SameSite: http.SameSiteLaxMode, MaxAge: int((10 * time.Minute).Seconds()),
	})
	http.Redirect(w, r, a.oidc.oauth.AuthCodeURL(state, oidc.Nonce(nonce), oauth2.S256ChallengeOption(verifier)), http.StatusFound)
}

func (a *Auth) oidcCallback(w http.ResponseWriter, r *http.Request) {
	if a.oidc == nil {
		http.NotFound(w, r)
		return
	}
	u, err := a.oidcUser(r)
	http.SetCookie(w, &http.Cookie{ //nolint:gosec // Secure follows the public URL scheme
		Name: oidcCookie, Value: "", Path: "/auth/oidc", MaxAge: -1, HttpOnly: true,
		Secure: a.publicURL.Scheme == "https", SameSite: http.SameSiteLaxMode,
	})
	if err != nil {
		a.log.WarnContext(r.Context(), "oidc sign-in refused", "err", err)
		http.Redirect(w, r, "/login?error=oidc", http.StatusSeeOther)
		return
	}
	if err := a.startSession(w, r, u, "oidc"); err != nil {
		http.Error(w, "could not sign in", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, a.landing(w, r), http.StatusSeeOther) //nolint:gosec // landing is a path on this server (localPath)
}

// ErrNotInvited means the provider confirmed an e-mail that has no account and no
// allowed domain.
var ErrNotInvited = errors.New("no account for this e-mail")

func (a *Auth) oidcUser(r *http.Request) (store.User, error) {
	c, err := r.Cookie(oidcCookie)
	if err != nil {
		return store.User{}, errors.New("sign-in expired; start again")
	}
	parts := strings.Split(c.Value, "|")
	if len(parts) != 3 || r.URL.Query().Get("state") != parts[0] {
		return store.User{}, errors.New("state mismatch")
	}
	if e := r.URL.Query().Get("error"); e != "" {
		return store.User{}, fmt.Errorf("provider: %s", e)
	}
	tok, err := a.oidc.oauth.Exchange(r.Context(), r.URL.Query().Get("code"), oauth2.VerifierOption(parts[2]))
	if err != nil {
		return store.User{}, fmt.Errorf("code exchange: %w", err)
	}
	rawID, ok := tok.Extra("id_token").(string)
	if !ok {
		return store.User{}, errors.New("no id_token")
	}
	idToken, err := a.oidc.verifier.Verify(r.Context(), rawID)
	if err != nil {
		return store.User{}, fmt.Errorf("id_token: %w", err)
	}
	if idToken.Nonce != parts[1] {
		return store.User{}, errors.New("nonce mismatch")
	}
	var claims struct {
		Email         string `json:"email"`
		EmailVerified *bool  `json:"email_verified"`
		Name          string `json:"name"`
	}
	if err := idToken.Claims(&claims); err != nil {
		return store.User{}, err
	}
	if claims.Email == "" || (claims.EmailVerified != nil && !*claims.EmailVerified) {
		return store.User{}, errors.New("provider did not confirm an e-mail address")
	}
	var raw map[string]any
	if err := idToken.Claims(&raw); err != nil {
		return store.User{}, err
	}
	return a.oidcAccount(r.Context(), claims.Email, claims.Name, groupsOf(raw[a.oidc.claim]))
}

// groupsOf reads a groups claim: a list of names (or IDs, as Entra ID sends), or one name.
func groupsOf(v any) []string {
	switch g := v.(type) {
	case string:
		return []string{g}
	case []any:
		var out []string
		for _, x := range g {
			if s, ok := x.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

// oidcAccount finds or creates the account of a person the provider confirmed. With group roles, their groups
// decide whether they may sign in, and their roles, every time; without, an invitation or an allowed e-mail domain
// does, and roles are set in Goliash.
func (a *Auth) oidcAccount(ctx context.Context, email, name string, groups []string) (store.User, error) {
	ws, err := a.store.ListWorkspaces(ctx)
	if err != nil || len(ws) == 0 {
		return store.User{}, errors.New("no organization")
	}
	orgID := ws[0].OrgID
	if len(a.oidc.groups) > 0 {
		u, err := a.store.GetUserByEmail(ctx, orgID, email)
		if err == nil { // the groups SCIM pushed count too, for providers that send no groups claim
			if pushed, err := a.store.UserSCIMGroups(ctx, u.ID); err == nil {
				for _, g := range pushed {
					groups = append(groups, g.DisplayName, g.ExternalID)
				}
			}
		}
		grants, ok := a.oidc.groups.For(groups)
		switch {
		case err == nil && u.Role == store.RoleOwner:
			return u, nil // owners stay owners, whatever their groups
		case err == nil && !ok:
			return store.User{}, fmt.Errorf("%w: %s", ErrNoGroup, email)
		case errors.Is(err, store.ErrNotFound) && !ok:
			return store.User{}, fmt.Errorf("%w: %s", ErrNoGroup, email)
		case errors.Is(err, store.ErrNotFound):
			if u, err = a.store.CreateUser(ctx, orgID, email, name, grants.OrgRole); err != nil {
				return u, err
			}
		case err != nil:
			return store.User{}, err
		}
		if u.Disabled {
			return store.User{}, fmt.Errorf("%w: %s", ErrDisabled, email)
		}
		if err := ApplyGrants(ctx, a.store, u, grants); err != nil {
			return u, err
		}
		u.Role = grants.OrgRole
		return u, nil
	}
	return a.invitedAccount(ctx, orgID, ws[0].ID, email, name)
}

// invitedAccount is the account of an invited person, or a new viewer from an allowed e-mail domain.
func (a *Auth) invitedAccount(ctx context.Context, orgID, firstWorkspace, email, name string) (store.User, error) {
	u, err := a.store.GetUserByEmail(ctx, orgID, email)
	if err == nil && u.Disabled {
		return store.User{}, fmt.Errorf("%w: %s", ErrDisabled, email)
	}
	if err == nil {
		return u, nil
	}
	if !errors.Is(err, store.ErrNotFound) {
		return store.User{}, err
	}
	_, domain, _ := strings.Cut(strings.ToLower(email), "@")
	for _, d := range a.oidc.domains {
		if strings.EqualFold(strings.TrimSpace(d), domain) {
			// Viewers of the organization's first workspace; admins can widen that.
			u, err := a.store.CreateUser(ctx, orgID, email, name, store.RoleViewer)
			if err != nil {
				return u, err
			}
			return u, a.store.SetMembership(ctx, u.ID, firstWorkspace, store.RoleViewer)
		}
	}
	return store.User{}, fmt.Errorf("%w: %s", ErrNotInvited, email)
}
