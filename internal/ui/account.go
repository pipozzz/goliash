// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package ui

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/pipozzz/goliash/internal/auth"
	"github.com/pipozzz/goliash/internal/store"
)

// AccountView is the signed-in person's own page.
type AccountView struct {
	Base
	Name             string
	HasPassword      bool
	PasswordChanged  time.Time
	PasswordsEnabled bool
	NeedCurrent      bool
	MinLength        int
	Sessions         []SessionView
	TOTP             store.TOTP
	PasskeysEnabled  bool
	Passkeys         []PasskeyView
}

// SessionView is one signed-in browser.
type SessionView struct {
	ID        string
	Device    string
	IP        string
	Method    string
	CreatedAt time.Time
	LastSeen  time.Time
	Current   bool
}

// recentSignIn is how long after signing in with a link or single sign-on a person
// may set a new password without the current one (that is how a reset works).
const recentSignIn = 15 * time.Minute

// mayResetPassword reports whether the current session proves the person just now:
// a sign-in link or single sign-on in the last few minutes.
func mayResetPassword(cur store.Session, now time.Time) bool {
	// "link+totp" and the like: a link or single sign-on followed by a second factor.
	first, _, _ := strings.Cut(cur.Method, "+")
	return (first == "link" || first == "oidc") && now.Sub(cur.CreatedAt) < recentSignIn
}

func (s *Server) currentSession(r *http.Request, p auth.Principal) (store.Session, []store.Session, error) {
	all, err := s.store.ListSessions(r.Context(), p.User.ID)
	if err != nil {
		return store.Session{}, nil, err
	}
	id := auth.SessionID(r)
	for _, x := range all {
		if x.ID == id {
			return x, all, nil
		}
	}
	return store.Session{}, all, nil
}

func (s *Server) account(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	cur, all, err := s.currentSession(r, p)
	if err != nil {
		return err
	}
	v := AccountView{
		Base: withFlash(s.base(r.Context(), p, "account", "Your account"), r),
		Name: p.User.Name, HasPassword: p.User.HasPassword, PasswordChanged: p.User.PasswordChangedAt,
		PasswordsEnabled: s.auth.PasswordsEnabled(), MinLength: auth.MinPasswordLength,
		NeedCurrent: p.User.HasPassword && !mayResetPassword(cur, time.Now()),
	}
	if v.TOTP, err = s.store.UserTOTP(r.Context(), p.User.ID); err != nil {
		return err
	}
	v.TOTP.Secret = "" // never rendered
	v.PasskeysEnabled = s.auth.PasskeysEnabled()
	keys, err := s.store.ListPasskeys(r.Context(), p.User.ID)
	if err != nil {
		return err
	}
	for _, k := range keys {
		v.Passkeys = append(v.Passkeys, PasskeyView{ID: k.ID, Name: k.Name, CreatedAt: k.CreatedAt, LastUsed: k.LastUsedAt})
	}
	if r.URL.Query().Get("passkey") == "added" && v.Notice == "" {
		v.Notice = "Passkey added. Next time, sign in with it: no password or code needed."
	}
	for _, x := range all {
		v.Sessions = append(v.Sessions, SessionView{
			ID: x.ID, Device: device(x.UserAgent), IP: x.IP, Method: methodLabel(x.Method),
			CreatedAt: x.CreatedAt, LastSeen: x.LastSeen, Current: x.ID == cur.ID,
		})
	}
	return render(w, r, AccountPage(v))
}

func (s *Server) saveName(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	name := strings.TrimSpace(r.FormValue("name"))
	if len(name) > 100 {
		return back(w, r, "/account", "error", "Use at most 100 characters.")
	}
	if err := s.store.SetUserName(r.Context(), p.User.ID, name); err != nil {
		return err
	}
	return back(w, r, "/account", "notice", "Saved.")
}

func (s *Server) changePassword(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	if !s.auth.PasswordsEnabled() {
		return back(w, r, "/account", "error", "Password sign-in is turned off on this server.")
	}
	ctx := r.Context()
	password := r.FormValue("password")
	if password != r.FormValue("confirm") {
		return back(w, r, "/account", "error", "The two passwords differ.")
	}
	if err := auth.CheckPassword(password, p.User.Email); err != nil {
		return back(w, r, "/account", "error", "Choose another password: "+strings.TrimPrefix(err.Error(), auth.ErrWeakPassword.Error()+": ")+".")
	}
	cur, _, err := s.currentSession(r, p)
	if err != nil {
		return err
	}
	if p.User.HasPassword && !mayResetPassword(cur, time.Now()) {
		hash, err := s.store.UserPasswordHash(ctx, p.User.ID)
		if err != nil {
			return err
		}
		if !auth.VerifyPassword(r.FormValue("current"), hash) {
			s.audit(ctx, p, "user.password_change_failed")
			return back(w, r, "/account", "error", "The current password is wrong.")
		}
	}
	if err := s.store.SetUserPassword(ctx, p.User.ID, auth.HashPassword(password), cur.ID); err != nil {
		return err
	}
	action := "user.password_set"
	if p.User.HasPassword {
		action = "user.password_change"
	}
	s.audit(ctx, p, action)
	return back(w, r, "/account", "notice", "Password saved. Your other devices are signed out.")
}

func (s *Server) signOutSession(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	err := s.store.DeleteUserSession(r.Context(), p.User.ID, r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		return back(w, r, "/account", "error", "That device is already signed out.")
	}
	if err != nil {
		return err
	}
	s.audit(r.Context(), p, "user.sign_out_device")
	return back(w, r, "/account", "notice", "Signed out.")
}

func (s *Server) signOutOthers(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	n, err := s.store.DeleteUserSessions(r.Context(), p.User.ID, auth.SessionID(r))
	if err != nil {
		return err
	}
	s.audit(r.Context(), p, "user.sign_out_others")
	return back(w, r, "/account", "notice", "Signed out "+itoa(n)+" other device(s).")
}

// signOutUser signs a person out everywhere (admins, from the Users page).
func (s *Server) signOutUser(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	u, _, ok := s.manageable(r.Context(), p, r.PathValue("id"))
	if !ok {
		return back(w, r, "/settings", "error", "Unknown user.")
	}
	n, err := s.store.DeleteUserSessions(r.Context(), u.ID, "")
	if err != nil {
		return err
	}
	s.audit(r.Context(), p, "user.sign_out_everywhere", "user", u.Email)
	return back(w, r, "/settings", "notice", u.Email+" signed out of "+itoa(n)+" device(s).")
}

// removePassword takes a person's password away (admins), e.g. when it leaked.
// They sign in with a link or single sign-on and may set a new one.
func (s *Server) removePassword(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	u, _, ok := s.manageable(r.Context(), p, r.PathValue("id"))
	if !ok {
		return back(w, r, "/settings", "error", "Unknown user.")
	}
	if err := s.store.SetUserPassword(r.Context(), u.ID, "", ""); err != nil {
		return err
	}
	s.audit(r.Context(), p, "user.password_remove", "user", u.Email)
	return back(w, r, "/settings", "notice", "Password of "+u.Email+" removed and every device signed out.")
}

func methodLabel(m string) string {
	if first, second, ok := strings.Cut(m, "+"); ok {
		return methodLabel(first) + " + " + map[string]string{"totp": "app code", "recovery code": "recovery code", "passkey": "passkey"}[second]
	}
	switch m {
	case "recovery":
		return "recovery link"
	case "password":
		return "password"
	case "link":
		return "sign-in link"
	case "oidc":
		return "single sign-on"
	case "passkey":
		return "passkey"
	}
	return ""
}

// device names a browser from its User-Agent, roughly: "Firefox on macOS".
func device(ua string) string {
	if ua == "" {
		return "Unknown device"
	}
	browser := "Browser"
	for _, b := range []struct{ token, name string }{
		{"Edg/", "Edge"},
		{"OPR/", "Opera"},
		{"Firefox/", "Firefox"},
		{"Chrome/", "Chrome"},
		{"Safari/", "Safari"},
		{"curl/", "curl"},
	} {
		if strings.Contains(ua, b.token) {
			browser = b.name
			break
		}
	}
	for _, o := range []struct{ token, name string }{
		{"iPhone", "iPhone"},
		{"iPad", "iPad"},
		{"Android", "Android"},
		{"Mac OS X", "macOS"},
		{"Windows", "Windows"},
		{"CrOS", "ChromeOS"},
		{"Linux", "Linux"},
	} {
		if strings.Contains(ua, o.token) {
			return browser + " on " + o.name
		}
	}
	return browser
}
