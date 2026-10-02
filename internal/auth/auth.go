// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

// Package auth signs people in (magic links, OIDC), keeps browser sessions and
// authenticates API tokens. Every request resolves to a Principal.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/pipozzz/goliash/internal/store"
	"github.com/pipozzz/goliash/internal/tokens"
)

const (
	sessionCookie = "goliash_session"
	sessionTTL    = 30 * 24 * time.Hour
	loginTokenTTL = 15 * time.Minute
)

// Principal is who a request acts as.
type Principal struct {
	User  store.User // zero for API tokens
	Scope store.Scope
	Role  string
	Via   string // session or token
}

// Can reports whether the principal has at least the given role.
func (p Principal) Can(role string) bool { return store.RoleRank(p.Role) >= store.RoleRank(role) }

// Name is how the principal is shown in audit trails and acks.
func (p Principal) Name() string {
	if p.User.Email != "" {
		return p.User.Email
	}
	return "api token"
}

type ctxKey struct{}

// FromContext returns the principal of an authenticated request.
func FromContext(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(ctxKey{}).(Principal)
	return p, ok
}

// WithPrincipal stores a principal in a context (tests and middleware).
func WithPrincipal(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, ctxKey{}, p)
}

// MailFunc sends a sign-in e-mail. Nil means e-mail is not configured.
type MailFunc func(ctx context.Context, to, subject, body string) error

// Auth holds sign-in configuration.
type Auth struct {
	store     *store.Store
	log       *slog.Logger
	publicURL *url.URL
	mail      MailFunc
	oidc      *OIDC
}

// New returns an Auth. publicURL is where people reach the server (for links and
// cookies); mail may be nil.
func New(st *store.Store, log *slog.Logger, publicURL string, mail MailFunc) (*Auth, error) {
	u, err := url.Parse(strings.TrimSuffix(publicURL, "/"))
	if err != nil || u.Scheme == "" || u.Host == "" {
		return nil, fmt.Errorf("public URL %q must be absolute, e.g. https://goliash.example.com", publicURL)
	}
	return &Auth{store: st, log: log, publicURL: u, mail: mail}, nil
}

// SetOIDC enables OIDC sign-in.
func (a *Auth) SetOIDC(o *OIDC) { a.oidc = o }

// OIDCEnabled reports whether OIDC sign-in is configured.
func (a *Auth) OIDCEnabled() bool { return a.oidc != nil }

// OIDCName is the provider name shown on the sign-in button.
func (a *Auth) OIDCName() string {
	if a.oidc == nil {
		return ""
	}
	return a.oidc.name
}

// MailEnabled reports whether magic links can be e-mailed.
func (a *Auth) MailEnabled() bool { return a.mail != nil }

func randomToken() (raw, hash string) {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	raw = base64.RawURLEncoding.EncodeToString(b)
	sum := sha256.Sum256([]byte(raw))
	return raw, hex.EncodeToString(sum[:])
}

func hashOf(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

// LoginLink creates a single-use sign-in URL for a user, valid for 15 minutes.
func (a *Auth) LoginLink(ctx context.Context, u store.User) (string, error) {
	raw, hash := randomToken()
	if err := a.store.CreateLoginToken(ctx, hash, u.ID, loginTokenTTL); err != nil {
		return "", err
	}
	return a.publicURL.String() + "/auth/magic?token=" + url.QueryEscape(raw), nil
}

// workspaceOf returns the workspace a user works in. Self-hosted has one.
func (a *Auth) workspaceOf(ctx context.Context, u store.User) (store.Scope, error) {
	all, err := a.store.ListWorkspaces(ctx)
	if err != nil {
		return store.Scope{}, err
	}
	for _, w := range all {
		if w.OrgID == u.OrgID {
			return w.Scope(), nil
		}
	}
	return store.Scope{}, errors.New("organization has no workspace")
}

// Authenticate resolves a request's principal from its session cookie or its
// bearer API token. ok is false for anonymous requests.
func (a *Auth) Authenticate(r *http.Request) (Principal, bool, error) {
	if token, found := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); found {
		if !tokens.Valid(token, tokens.API) {
			return Principal{}, false, nil
		}
		sc, err := a.store.APITokenScope(r.Context(), tokens.Hash(token))
		if errors.Is(err, store.ErrNotFound) {
			return Principal{}, false, nil
		}
		if err != nil {
			return Principal{}, false, err
		}
		return Principal{Scope: sc, Role: store.RoleAdmin, Via: "token"}, true, nil
	}
	c, err := r.Cookie(sessionCookie)
	if err != nil || c.Value == "" {
		return Principal{}, false, nil
	}
	u, err := a.store.SessionUser(r.Context(), hashOf(c.Value))
	if errors.Is(err, store.ErrNotFound) {
		return Principal{}, false, nil
	}
	if err != nil {
		return Principal{}, false, err
	}
	sc, err := a.workspaceOf(r.Context(), u)
	if err != nil {
		return Principal{}, false, err
	}
	return Principal{User: u, Scope: sc, Role: u.Role, Via: "session"}, true, nil
}

// startSession signs a user in on this browser.
func (a *Auth) startSession(w http.ResponseWriter, r *http.Request, u store.User) error {
	raw, hash := randomToken()
	if err := a.store.CreateSession(r.Context(), hash, u.ID, sessionTTL); err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{ //nolint:gosec // Secure follows the public URL scheme; plain http is for local use
		Name: sessionCookie, Value: raw, Path: "/", HttpOnly: true, Secure: a.publicURL.Scheme == "https",
		SameSite: http.SameSiteLaxMode, MaxAge: int(sessionTTL / time.Second),
	})
	a.log.InfoContext(r.Context(), "signed in", "user", u.Email)
	if err := a.store.Audit(r.Context(), store.AuditEntry{OrgID: u.OrgID, Actor: u.Email, Action: "user.sign_in"}); err != nil {
		a.log.ErrorContext(r.Context(), "audit log write failed", "err", err)
	}
	return nil
}

// Routes adds the sign-in endpoints to mux:
//
//	POST /auth/magic      e-mail a sign-in link (form field "email")
//	GET  /auth/magic      sign in with a link
//	GET  /auth/oidc/start, /auth/oidc/callback
//	POST /auth/logout
func (a *Auth) Routes(mux *http.ServeMux) {
	mux.HandleFunc("POST /auth/magic", a.requestLink)
	mux.HandleFunc("GET /auth/magic", a.useLink)
	mux.HandleFunc("GET /auth/oidc/start", a.oidcStart)
	mux.HandleFunc("GET /auth/oidc/callback", a.oidcCallback)
	mux.HandleFunc("POST /auth/logout", a.logout)
}

func (a *Auth) requestLink(w http.ResponseWriter, r *http.Request) {
	email := strings.TrimSpace(r.FormValue("email"))
	// Always answer the same way, so the form does not reveal who has an account.
	defer http.Redirect(w, r, "/login?sent=1", http.StatusSeeOther)
	if a.mail == nil || email == "" {
		return
	}
	ws, err := a.store.ListWorkspaces(r.Context())
	if err != nil || len(ws) == 0 {
		return
	}
	u, err := a.store.GetUserByEmail(r.Context(), ws[0].OrgID, email)
	if err != nil {
		return
	}
	link, err := a.LoginLink(r.Context(), u)
	if err != nil {
		a.log.ErrorContext(r.Context(), "creating sign-in link failed", "err", err)
		return
	}
	body := "Sign in to Goliash:\r\n\r\n" + link + "\r\n\r\nThe link works once and expires in 15 minutes. " +
		"If you did not ask for it, ignore this e-mail.\r\n"
	if err := a.mail(r.Context(), u.Email, "Your Goliash sign-in link", body); err != nil {
		a.log.ErrorContext(r.Context(), "sending sign-in link failed", "err", err)
	}
}

func (a *Auth) useLink(w http.ResponseWriter, r *http.Request) {
	u, err := a.store.ConsumeLoginToken(r.Context(), hashOf(r.URL.Query().Get("token")))
	if err != nil {
		http.Redirect(w, r, "/login?error=link", http.StatusSeeOther)
		return
	}
	if err := a.startSession(w, r, u); err != nil {
		http.Error(w, "could not sign in", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (a *Auth) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		_ = a.store.DeleteSession(r.Context(), hashOf(c.Value))
	}
	http.SetCookie(w, &http.Cookie{ //nolint:gosec // Secure follows the public URL scheme
		Name: sessionCookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true,
		Secure: a.publicURL.Scheme == "https", SameSite: http.SameSiteLaxMode,
	})
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}
