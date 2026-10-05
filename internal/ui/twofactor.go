// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package ui

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"
	"time"

	"rsc.io/qr"

	"github.com/pipozzz/goliash/internal/auth"
	"github.com/pipozzz/goliash/internal/store"
)

// SecondFactorView is the code step of signing in.
type SecondFactorView struct {
	Email    string
	Error    string
	Recovery bool
}

// TwoFactorSetupView sets up an authenticator app, then shows the recovery codes.
type TwoFactorSetupView struct {
	Base
	QR     string // data: URI of a PNG
	Secret string
	Codes  []string
}

func (s *Server) secondFactorPage(w http.ResponseWriter, r *http.Request) {
	u, ok := s.auth.SecondFactorPending(r)
	if !ok {
		http.Redirect(w, r, "/login?error=expired", http.StatusSeeOther)
		return
	}
	v := SecondFactorView{Email: u.Email, Recovery: r.URL.Query().Get("use") == "recovery"}
	if r.URL.Query().Get("error") == "code" {
		v.Error = "That code did not work. Codes change every 30 seconds and work once."
	}
	w.Header().Set("Cache-Control", "no-store")
	_ = render(w, r, SecondFactorPage(v))
}

func recoveryHashes(codes []string) []string {
	out := make([]string, len(codes))
	for i, c := range codes {
		sum := sha256.Sum256([]byte(auth.NormalizeRecoveryCode(c)))
		out[i] = hex.EncodeToString(sum[:])
	}
	return out
}

func (s *Server) startTwoFactor(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	if err := s.store.StartTOTP(r.Context(), p.User.ID, auth.NewTOTPSecret()); errors.Is(err, store.ErrExists) {
		return back(w, r, "/account", "error", "Two-factor sign-in is on already.")
	} else if err != nil {
		return err
	}
	http.Redirect(w, r, "/account/2fa", http.StatusSeeOther)
	return nil
}

func (s *Server) twoFactorSetup(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	t, err := s.store.UserTOTP(r.Context(), p.User.ID)
	if err != nil {
		return err
	}
	if t.Enabled || t.Secret == "" {
		http.Redirect(w, r, "/account", http.StatusSeeOther)
		return nil
	}
	code, err := qr.Encode(auth.TOTPURI(t.Secret, "Goliash", p.User.Email), qr.M)
	if err != nil {
		return err
	}
	v := TwoFactorSetupView{
		Base: withFlash(s.base(r.Context(), p, "account", "Two-factor sign-in"), r), Secret: spaced(t.Secret),
		QR: "data:image/png;base64," + base64.StdEncoding.EncodeToString(code.PNG()),
	}
	w.Header().Set("Cache-Control", "no-store")
	return render(w, r, TwoFactorSetupPage(v))
}

// spaced groups a key in fours for typing: ABCD EFGH …
func spaced(key string) string {
	var b strings.Builder
	for i, c := range key {
		if i > 0 && i%4 == 0 {
			b.WriteByte(' ')
		}
		b.WriteRune(c)
	}
	return b.String()
}

func (s *Server) enableTwoFactor(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	ctx := r.Context()
	t, err := s.store.UserTOTP(ctx, p.User.ID)
	if err != nil {
		return err
	}
	if t.Enabled || t.Secret == "" {
		return back(w, r, "/account", "error", "Start the setup again.")
	}
	step, ok := auth.VerifyTOTP(t.Secret, r.FormValue("code"), time.Now())
	if !ok {
		return back(w, r, "/account/2fa", "error", "That code did not work. Check the time on your phone and try the next code.")
	}
	codes := auth.NewRecoveryCodes(10)
	if err := s.store.EnableTOTP(ctx, p.User.ID, step, recoveryHashes(codes)); err != nil {
		return err
	}
	s.audit(ctx, p, "user.2fa_on")
	v := TwoFactorSetupView{Base: s.base(ctx, p, "account", "Two-factor sign-in"), Codes: codes}
	v.Notice = "Two-factor sign-in is on."
	w.Header().Set("Cache-Control", "no-store")
	return render(w, r, TwoFactorSetupPage(v))
}

// checkCode accepts a current app code (once) or, when allowRecovery, a recovery code.
func (s *Server) checkCode(r *http.Request, userID, code string, allowRecovery bool) (bool, error) {
	t, err := s.store.UserTOTP(r.Context(), userID)
	if err != nil || !t.Enabled {
		return false, err
	}
	if step, ok := auth.VerifyTOTP(t.Secret, code, time.Now()); ok {
		return s.store.UseTOTPStep(r.Context(), userID, step)
	}
	if !allowRecovery {
		return false, nil
	}
	return s.store.UseRecoveryCode(r.Context(), userID, recoveryHashes([]string{code})[0])
}

func (s *Server) newRecoveryCodes(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	ok, err := s.checkCode(r, p.User.ID, r.FormValue("code"), false)
	if err != nil {
		return err
	}
	if !ok {
		return back(w, r, "/account", "error", "That code did not work.")
	}
	codes := auth.NewRecoveryCodes(10)
	if err := s.store.ReplaceRecoveryCodes(r.Context(), p.User.ID, recoveryHashes(codes)); err != nil {
		return err
	}
	s.audit(r.Context(), p, "user.2fa_recovery_codes")
	v := TwoFactorSetupView{Base: s.base(r.Context(), p, "account", "Recovery codes"), Codes: codes}
	v.Notice = "New recovery codes; the old ones no longer work."
	w.Header().Set("Cache-Control", "no-store")
	return render(w, r, TwoFactorSetupPage(v))
}

func (s *Server) disableTwoFactor(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	ok, err := s.checkCode(r, p.User.ID, r.FormValue("code"), true)
	if err != nil {
		return err
	}
	if !ok {
		return back(w, r, "/account", "error", "That code did not work.")
	}
	if err := s.store.DisableTOTP(r.Context(), p.User.ID); err != nil {
		return err
	}
	s.audit(r.Context(), p, "user.2fa_off")
	return back(w, r, "/account", "notice", "Two-factor sign-in is off.")
}

// resetTwoFactor turns someone's two-factor sign-in off (admins), e.g. after a lost
// phone without recovery codes, and signs them out everywhere.
func (s *Server) resetTwoFactor(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	u, _, ok := s.manageable(r.Context(), p, r.PathValue("id"))
	if !ok {
		return back(w, r, "/settings", "error", "Unknown user.")
	}
	if err := s.store.DisableTOTP(r.Context(), u.ID); err != nil {
		return err
	}
	if _, err := s.store.DeleteUserSessions(r.Context(), u.ID, ""); err != nil {
		return err
	}
	s.audit(r.Context(), p, "user.2fa_reset", "user", u.Email)
	return back(w, r, "/settings", "notice", "Two-factor sign-in of "+u.Email+" is off and every device signed out. They can set it up again.")
}

// needsTwoFactor reports whether a session must set up two-factor sign-in before
// anything but its account page: the organization requires it, the person has none,
// and they signed in with a password or a link (single sign-on brings its own).
func (s *Server) needsTwoFactor(r *http.Request, p auth.Principal) bool {
	if p.Via != "session" || p.User.TOTPEnabled || strings.HasPrefix(p.Method, "oidc") {
		return false
	}
	switch r.URL.Path {
	case "/account", "/account/2fa", "/account/2fa/setup", "/account/2fa/enable":
		return false
	}
	required, err := s.store.RequireTwoFactor(r.Context(), p.User.OrgID)
	return err == nil && required
}

// setRequireTwoFactor turns the organization's requirement on or off (owners).
func (s *Server) setRequireTwoFactor(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	if p.User.Role != store.RoleOwner {
		s.problem(w, r, http.StatusForbidden, "Not allowed", "Only owners decide whether the organization requires two-factor sign-in.", true)
		return nil
	}
	on := r.FormValue("require") == "true"
	if on && !p.User.TOTPEnabled {
		return back(w, r, "/settings", "error", "Set up two-factor sign-in for yourself first, on your account page.")
	}
	if err := s.store.SetRequireTwoFactor(r.Context(), p.User.OrgID, on); err != nil {
		return err
	}
	if on {
		s.audit(r.Context(), p, "org.require_2fa", "require", "on")
		return back(w, r, "/settings", "notice", "Two-factor sign-in is now required. People without it set it up at their next page.")
	}
	s.audit(r.Context(), p, "org.require_2fa", "require", "off")
	return back(w, r, "/settings", "notice", "Two-factor sign-in is optional again.")
}
