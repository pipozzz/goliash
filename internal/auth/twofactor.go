// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package auth

import (
	"net/http"
	"time"

	"github.com/pipozzz/goliash/internal/store"
)

// Two-factor sign-in. After the first factor (a password or a sign-in link), a
// person with TOTP on gets a short-lived cookie instead of a session, and enters a
// code from their app, or a recovery code, at /login/2fa.

const (
	mfaCookie = "goliash_mfa"
	mfaTTL    = 5 * time.Minute
	// mfaFailures is how many wrong codes end the attempt; the person starts over.
	mfaFailures = 5
)

func (a *Auth) secondFactor(w http.ResponseWriter, r *http.Request, u store.User, method string) {
	raw, hash := randomToken()
	if err := a.store.CreateLoginToken(r.Context(), hash, u.ID, mfaTTL, store.TokenSecondFactor, method); err != nil {
		http.Error(w, "could not sign in", http.StatusInternalServerError)
		return
	}
	http.SetCookie(w, &http.Cookie{ //nolint:gosec // Secure follows the public URL scheme
		Name: mfaCookie, Value: raw, Path: "/", HttpOnly: true, Secure: a.publicURL.Scheme == "https",
		SameSite: http.SameSiteLaxMode, MaxAge: int(mfaTTL / time.Second),
	})
	http.Redirect(w, r, "/login/2fa", http.StatusSeeOther)
}

func (a *Auth) clearMFA(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{ //nolint:gosec // Secure follows the public URL scheme
		Name: mfaCookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true,
		Secure: a.publicURL.Scheme == "https", SameSite: http.SameSiteLaxMode,
	})
}

// SecondFactorPending reports whether the request carries a first factor waiting for
// its code, and for whom.
func (a *Auth) SecondFactorPending(r *http.Request) (store.User, bool) {
	c, err := r.Cookie(mfaCookie)
	if err != nil || c.Value == "" {
		return store.User{}, false
	}
	u, _, _, err := a.store.PeekLoginToken(r.Context(), hashOf(c.Value), store.TokenSecondFactor)
	return u, err == nil
}

func (a *Auth) verifySecondFactor(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	c, err := r.Cookie(mfaCookie)
	if err != nil || c.Value == "" {
		http.Redirect(w, r, "/login?error=expired", http.StatusSeeOther)
		return
	}
	hash := hashOf(c.Value)
	u, _, method, err := a.store.PeekLoginToken(ctx, hash, store.TokenSecondFactor)
	if err != nil {
		a.clearMFA(w)
		http.Redirect(w, r, "/login?error=expired", http.StatusSeeOther)
		return
	}
	key := "2fa:" + u.ID
	t, err := a.store.UserTOTP(ctx, u.ID)
	if err != nil {
		http.Error(w, "could not sign in", http.StatusInternalServerError)
		return
	}
	ok, how := false, ""
	if code := r.FormValue("code"); code != "" {
		if step, valid := VerifyTOTP(t.Secret, code, time.Now()); valid && t.Enabled {
			ok, err = a.store.UseTOTPStep(ctx, u.ID, step)
			how = "totp"
		}
	} else if rc := NormalizeRecoveryCode(r.FormValue("recovery")); rc != "" {
		ok, err = a.store.UseRecoveryCode(ctx, u.ID, hashOf(rc))
		how = "recovery code"
	}
	if err != nil {
		http.Error(w, "could not sign in", http.StatusInternalServerError)
		return
	}
	if !ok {
		a.byEmail.fail(ctx, key)
		a.log.WarnContext(ctx, "second factor failed", "user", u.Email, "ip", a.ClientIP(r))
		if a.byEmail.recentCount(ctx, key) >= mfaFailures {
			_, _, _ = a.store.ConsumeLoginToken(ctx, hash, store.TokenSecondFactor)
			a.clearMFA(w)
			http.Redirect(w, r, "/login?error=throttled", http.StatusSeeOther)
			return
		}
		http.Redirect(w, r, "/login/2fa?error=code", http.StatusSeeOther)
		return
	}
	if _, _, err := a.store.ConsumeLoginToken(ctx, hash, store.TokenSecondFactor); err != nil {
		http.Redirect(w, r, "/login?error=expired", http.StatusSeeOther)
		return
	}
	a.byEmail.reset(ctx, key)
	a.clearMFA(w)
	if err := a.startSession(w, r, u, method+"+"+how); err != nil {
		http.Error(w, "could not sign in", http.StatusInternalServerError)
		return
	}
	if how == "recovery code" {
		a.log.WarnContext(ctx, "signed in with a recovery code", "user", u.Email, "left", t.Codes-1)
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}
