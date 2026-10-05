// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

// Package auth signs people in (passwords, magic links, OIDC), keeps browser sessions and
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
	"net"
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
	Role  string // effective role in Scope's workspace; empty without workspace access
	Via   string // session or token

	Workspace  store.Workspace // the workspace the request works in (sessions only)
	Workspaces []store.Access  // every workspace the user may open (sessions only)
}

// OrgWide reports whether the principal manages the whole organization (owner or admin).
func (p Principal) OrgWide() bool { return p.Via == "session" && store.OrgWide(p.User.Role) }

// WorkspaceCookie remembers which workspace a browser works in.
const WorkspaceCookie = "goliash_ws"

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

	passwords  bool
	trustProxy bool
	byClient   *limiter // failed password sign-ins per client address
	byEmail    *limiter // failed password sign-ins per e-mail
}

// New returns an Auth. publicURL is where people reach the server (for links and
// cookies); mail may be nil.
func New(st *store.Store, log *slog.Logger, publicURL string, mail MailFunc) (*Auth, error) {
	u, err := url.Parse(strings.TrimSuffix(publicURL, "/"))
	if err != nil || u.Scheme == "" || u.Host == "" {
		return nil, fmt.Errorf("public URL %q must be absolute, e.g. https://goliash.example.com", publicURL)
	}
	return &Auth{
		store: st, log: log, publicURL: u, mail: mail, passwords: true,
		byClient: newLimiter(30, 15*time.Minute), byEmail: newLimiter(8, 15*time.Minute),
	}, nil
}

// SetPasswordLogin turns password sign-in on (the default) or off, e.g. when
// everyone signs in through OIDC.
func (a *Auth) SetPasswordLogin(on bool) { a.passwords = on }

// PasswordsEnabled reports whether people may sign in with a password.
func (a *Auth) PasswordsEnabled() bool { return a.passwords }

// SetTrustProxy makes the client address come from X-Forwarded-For, for servers
// behind a reverse proxy. Without a proxy, a client could fake the header.
func (a *Auth) SetTrustProxy(on bool) { a.trustProxy = on }

// ClientIP is the address a request came from: the last X-Forwarded-For entry
// (added by the proxy) when proxies are trusted, else the connection's address.
func (a *Auth) ClientIP(r *http.Request) string {
	if a.trustProxy {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			parts := strings.Split(xff, ",")
			if ip := strings.TrimSpace(parts[len(parts)-1]); ip != "" {
				return ip
			}
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// SessionID returns the stored ID (a hash) of the request's session, or "".
func SessionID(r *http.Request) string {
	c, err := r.Cookie(sessionCookie)
	if err != nil || c.Value == "" {
		return ""
	}
	return hashOf(c.Value)
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

// workspaceFor picks the workspace a session works in: the one in the workspace
// cookie when the user may open it, else the first they may open.
func (a *Auth) workspaceFor(r *http.Request, u store.User) (store.Access, []store.Access, error) {
	all, err := a.store.UserWorkspaces(r.Context(), u)
	if err != nil || len(all) == 0 {
		return store.Access{}, all, err
	}
	if c, err := r.Cookie(WorkspaceCookie); err == nil {
		for _, acc := range all {
			if acc.Workspace.ID == c.Value {
				return acc, all, nil
			}
		}
	}
	return all[0], all, nil
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
	cur, all, err := a.workspaceFor(r, u)
	if err != nil {
		return Principal{}, false, err
	}
	return Principal{
		User: u, Scope: cur.Workspace.Scope(), Role: cur.Role, Via: "session",
		Workspace: cur.Workspace, Workspaces: all,
	}, true, nil
}

// startSession signs a user in on this browser.
func (a *Auth) startSession(w http.ResponseWriter, r *http.Request, u store.User, method string) error {
	raw, hash := randomToken()
	ua := r.UserAgent()
	if len(ua) > 300 {
		ua = ua[:300]
	}
	x := store.Session{Method: method, UserAgent: ua, IP: a.ClientIP(r)}
	if err := a.store.CreateSession(r.Context(), hash, u.ID, sessionTTL, x); err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{ //nolint:gosec // Secure follows the public URL scheme; plain http is for local use
		Name: sessionCookie, Value: raw, Path: "/", HttpOnly: true, Secure: a.publicURL.Scheme == "https",
		SameSite: http.SameSiteLaxMode, MaxAge: int(sessionTTL / time.Second),
	})
	a.log.InfoContext(r.Context(), "signed in", "user", u.Email, "method", method)
	if err := a.store.Audit(r.Context(), store.AuditEntry{
		OrgID: u.OrgID, Actor: u.Email, Action: "user.sign_in", Details: map[string]string{"method": method, "ip": x.IP},
	}); err != nil {
		a.log.ErrorContext(r.Context(), "audit log write failed", "err", err)
	}
	return nil
}

// Routes adds the sign-in endpoints to mux:
//
//	POST /auth/password   sign in with e-mail and password
//	POST /auth/magic      e-mail a sign-in link (form field "email")
//	GET  /auth/magic      sign in with a link
//	GET  /auth/oidc/start, /auth/oidc/callback
//	POST /auth/logout
func (a *Auth) Routes(mux *http.ServeMux) {
	mux.HandleFunc("POST /auth/password", a.passwordLogin)
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
	if err := a.startSession(w, r, u, "link"); err != nil {
		http.Error(w, "could not sign in", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// passwordLogin signs in with e-mail and password. Every failure answers the same
// and takes as long, so the form does not reveal who has an account or a password.
// Too many failures for an address or a client pause sign-in for 15 minutes.
func (a *Auth) passwordLogin(w http.ResponseWriter, r *http.Request) {
	if !a.passwords {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	email := strings.ToLower(strings.TrimSpace(r.FormValue("email")))
	password := r.FormValue("password")
	ip := a.ClientIP(r)
	if a.byClient.blocked(ip) || a.byEmail.blocked(email) {
		a.log.WarnContext(r.Context(), "password sign-in throttled", "email", email, "ip", ip)
		http.Redirect(w, r, "/login?error=throttled", http.StatusSeeOther)
		return
	}
	u, hash := a.passwordOf(r.Context(), email)
	ok := VerifyPassword(password, hash)
	if hash == "" {
		VerifyPassword(password, dummyHash)
		ok = false
	}
	if !ok {
		a.byClient.fail(ip)
		a.byEmail.fail(email)
		a.log.WarnContext(r.Context(), "password sign-in failed", "email", email, "ip", ip)
		http.Redirect(w, r, "/login?error=password", http.StatusSeeOther)
		return
	}
	a.byEmail.reset(email)
	if err := a.startSession(w, r, u, "password"); err != nil {
		http.Error(w, "could not sign in", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// passwordOf finds a user by e-mail and their password hash; hash is empty when
// there is no such user or they have no password.
func (a *Auth) passwordOf(ctx context.Context, email string) (store.User, string) {
	ws, err := a.store.ListWorkspaces(ctx)
	if err != nil || len(ws) == 0 || email == "" {
		return store.User{}, ""
	}
	u, err := a.store.GetUserByEmail(ctx, ws[0].OrgID, email)
	if err != nil {
		return store.User{}, ""
	}
	hash, err := a.store.UserPasswordHash(ctx, u.ID)
	if err != nil {
		return store.User{}, ""
	}
	return u, hash
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
