// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package auth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"

	"github.com/pipozzz/goliash/internal/store"
)

// Passkeys. A passkey signs a person in on its own, with no password and no second
// factor: it is bound to this server's address and proves the device and its
// unlock. It also stands in as the second factor after a password or a link.

const (
	webauthnCookie = "goliash_webauthn"
	ceremonyTTL    = 5 * time.Minute
)

// ErrNoPasskeys means passkeys cannot work on this server's address: browsers allow
// them only on a domain name over HTTPS, or on localhost.
var ErrNoPasskeys = errors.New("passkeys need the server on a domain name over HTTPS")

// newWebAuthn configures passkeys for the public URL, or returns nil when its
// address cannot carry them (an IP address, or plain HTTP other than localhost).
func newWebAuthn(publicURL string, host, scheme string) *webauthn.WebAuthn {
	if net.ParseIP(host) != nil || (scheme != "https" && host != "localhost") {
		return nil
	}
	w, err := webauthn.New(&webauthn.Config{
		RPID: host, RPDisplayName: "Goliash", RPOrigins: []string{publicURL},
	})
	if err != nil {
		return nil
	}
	return w
}

// PasskeysEnabled reports whether this server can offer passkeys.
func (a *Auth) PasskeysEnabled() bool { return a.webauthn != nil }

// passkeyUser is a user as the WebAuthn library sees them.
type passkeyUser struct {
	u     store.User
	creds []webauthn.Credential
}

func (p passkeyUser) WebAuthnID() []byte                         { return []byte(p.u.ID) }
func (p passkeyUser) WebAuthnName() string                       { return p.u.Email }
func (p passkeyUser) WebAuthnCredentials() []webauthn.Credential { return p.creds }
func (p passkeyUser) WebAuthnDisplayName() string {
	if p.u.Name != "" {
		return p.u.Name
	}
	return p.u.Email
}

func (a *Auth) passkeyUser(ctx context.Context, u store.User) (passkeyUser, error) {
	keys, err := a.store.ListPasskeys(ctx, u.ID)
	if err != nil {
		return passkeyUser{}, err
	}
	pu := passkeyUser{u: u}
	for _, k := range keys {
		var c webauthn.Credential
		if err := json.Unmarshal(k.Credential, &c); err != nil {
			return passkeyUser{}, err
		}
		pu.creds = append(pu.creds, c)
	}
	return pu, nil
}

// ceremony is what is kept between a ceremony's two requests.
type ceremony struct {
	Purpose string               `json:"purpose"` // login, second, register
	UserID  string               `json:"user_id,omitempty"`
	Session webauthn.SessionData `json:"session"`
}

func (a *Auth) startCeremony(w http.ResponseWriter, r *http.Request, c ceremony) error {
	data, err := json.Marshal(c)
	if err != nil {
		return err
	}
	raw, hash := randomToken()
	if err := a.store.SaveWebAuthnSession(r.Context(), hash, data, ceremonyTTL); err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{ //nolint:gosec // Secure follows the public URL scheme
		Name: webauthnCookie, Value: raw, Path: "/", HttpOnly: true, Secure: a.publicURL.Scheme == "https",
		SameSite: http.SameSiteStrictMode, MaxAge: int(ceremonyTTL / time.Second),
	})
	return nil
}

// takeCeremony returns the ceremony of the request's cookie, once.
func (a *Auth) takeCeremony(w http.ResponseWriter, r *http.Request, purpose string) (ceremony, error) {
	http.SetCookie(w, &http.Cookie{ //nolint:gosec // Secure follows the public URL scheme
		Name: webauthnCookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true,
		Secure: a.publicURL.Scheme == "https", SameSite: http.SameSiteStrictMode,
	})
	ck, err := r.Cookie(webauthnCookie)
	if err != nil || ck.Value == "" {
		return ceremony{}, store.ErrNotFound
	}
	data, err := a.store.TakeWebAuthnSession(r.Context(), hashOf(ck.Value))
	if err != nil {
		return ceremony{}, err
	}
	var c ceremony
	if err := json.Unmarshal(data, &c); err != nil || c.Purpose != purpose {
		return ceremony{}, store.ErrNotFound
	}
	return c, nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func passkeyError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// passkeyOptions starts a sign-in with any passkey of this server (the browser lets
// the person pick one).
func (a *Auth) passkeyOptions(w http.ResponseWriter, r *http.Request) {
	if a.webauthn == nil {
		passkeyError(w, http.StatusNotFound, ErrNoPasskeys.Error())
		return
	}
	opts, session, err := a.webauthn.BeginDiscoverableLogin(webauthn.WithUserVerification(protocol.VerificationRequired))
	if err == nil {
		err = a.startCeremony(w, r, ceremony{Purpose: "login", Session: *session})
	}
	if err != nil {
		a.log.ErrorContext(r.Context(), "passkey sign-in failed to start", "err", err)
		passkeyError(w, http.StatusInternalServerError, "Passkey sign-in could not start.")
		return
	}
	writeJSON(w, http.StatusOK, opts)
}

// passkeyLogin finishes a sign-in with a passkey: a session, without a second factor.
func (a *Auth) passkeyLogin(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if a.byClient.blocked(ctx, a.ClientIP(r)) {
		passkeyError(w, http.StatusTooManyRequests, "Too many attempts. Wait 15 minutes.")
		return
	}
	c, err := a.takeCeremony(w, r, "login")
	if err != nil {
		passkeyError(w, http.StatusBadRequest, "The sign-in expired. Try again.")
		return
	}
	var who store.User
	_, cred, err := a.webauthn.FinishPasskeyLogin(func(_, userHandle []byte) (webauthn.User, error) {
		u, err := a.store.GetUser(ctx, string(userHandle))
		if err != nil {
			return nil, err
		}
		who = u
		return a.passkeyUser(ctx, u)
	}, c.Session, r)
	if err != nil {
		a.byClient.fail(ctx, a.ClientIP(r))
		a.log.WarnContext(ctx, "passkey sign-in failed", "err", err, "ip", a.ClientIP(r))
		passkeyError(w, http.StatusUnauthorized, "That passkey is not known here. Sign in another way and add it under your account.")
		return
	}
	if err := a.usedPasskey(ctx, cred); err != nil {
		passkeyError(w, http.StatusUnauthorized, "That passkey is not known here.")
		return
	}
	if err := a.startSession(w, r, who, "passkey"); err != nil {
		passkeyError(w, http.StatusInternalServerError, "Could not sign in.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"redirect": "/"})
}

// usedPasskey stores a passkey's record after a sign-in (its sign count).
func (a *Auth) usedPasskey(ctx context.Context, cred *webauthn.Credential) error {
	data, err := json.Marshal(cred)
	if err != nil {
		return err
	}
	return a.store.UsePasskey(ctx, base64.RawURLEncoding.EncodeToString(cred.ID), data)
}

// secondPasskeyOptions starts the second factor with one of the person's passkeys.
func (a *Auth) secondPasskeyOptions(w http.ResponseWriter, r *http.Request) {
	u, ok := a.SecondFactorPending(r)
	if !ok || a.webauthn == nil {
		passkeyError(w, http.StatusBadRequest, "The sign-in step expired. Start again.")
		return
	}
	pu, err := a.passkeyUser(r.Context(), u)
	if err != nil || len(pu.creds) == 0 {
		passkeyError(w, http.StatusBadRequest, "You have no passkey here.")
		return
	}
	opts, session, err := a.webauthn.BeginLogin(pu, webauthn.WithUserVerification(protocol.VerificationPreferred))
	if err == nil {
		err = a.startCeremony(w, r, ceremony{Purpose: "second", UserID: u.ID, Session: *session})
	}
	if err != nil {
		passkeyError(w, http.StatusInternalServerError, "Passkey sign-in could not start.")
		return
	}
	writeJSON(w, http.StatusOK, opts)
}

// secondPasskey finishes the second factor with a passkey.
func (a *Auth) secondPasskey(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	mfa, err := r.Cookie(mfaCookie)
	if err != nil || mfa.Value == "" {
		passkeyError(w, http.StatusBadRequest, "The sign-in step expired. Start again.")
		return
	}
	u, _, method, err := a.store.PeekLoginToken(ctx, hashOf(mfa.Value), store.TokenSecondFactor)
	if err != nil {
		passkeyError(w, http.StatusBadRequest, "The sign-in step expired. Start again.")
		return
	}
	c, err := a.takeCeremony(w, r, "second")
	if err != nil || c.UserID != u.ID {
		passkeyError(w, http.StatusBadRequest, "The sign-in step expired. Try again.")
		return
	}
	pu, err := a.passkeyUser(ctx, u)
	if err != nil {
		passkeyError(w, http.StatusInternalServerError, "Could not sign in.")
		return
	}
	cred, err := a.webauthn.FinishLogin(pu, c.Session, r)
	if err != nil {
		a.byEmail.fail(ctx, "2fa:"+u.ID)
		a.log.WarnContext(ctx, "passkey second factor failed", "user", u.Email, "err", err)
		passkeyError(w, http.StatusUnauthorized, "That passkey did not work. Try again, or use a code.")
		return
	}
	if err := a.usedPasskey(ctx, cred); err != nil {
		passkeyError(w, http.StatusUnauthorized, "That passkey is not known here.")
		return
	}
	if _, _, err := a.store.ConsumeLoginToken(ctx, hashOf(mfa.Value), store.TokenSecondFactor); err != nil {
		passkeyError(w, http.StatusBadRequest, "The sign-in step expired. Start again.")
		return
	}
	a.clearMFA(w)
	if err := a.startSession(w, r, u, method+"+passkey"); err != nil {
		passkeyError(w, http.StatusInternalServerError, "Could not sign in.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"redirect": "/"})
}

// PasskeyRegistrationOptions starts adding a passkey for a signed-in person and
// writes the options for the browser.
func (a *Auth) PasskeyRegistrationOptions(w http.ResponseWriter, r *http.Request, u store.User) error {
	if a.webauthn == nil {
		return ErrNoPasskeys
	}
	pu, err := a.passkeyUser(r.Context(), u)
	if err != nil {
		return err
	}
	exclude := make([]protocol.CredentialDescriptor, len(pu.creds))
	for i, c := range pu.creds {
		exclude[i] = c.Descriptor()
	}
	opts, session, err := a.webauthn.BeginRegistration(pu,
		webauthn.WithResidentKeyRequirement(protocol.ResidentKeyRequirementRequired),
		webauthn.WithExclusions(exclude),
		webauthn.WithAuthenticatorSelection(protocol.AuthenticatorSelection{
			ResidentKey: protocol.ResidentKeyRequirementRequired, UserVerification: protocol.VerificationRequired,
		}))
	if err != nil {
		return err
	}
	if err := a.startCeremony(w, r, ceremony{Purpose: "register", UserID: u.ID, Session: *session}); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, opts)
	return nil
}

// AddPasskey finishes adding a passkey from the browser's answer in the request body.
func (a *Auth) AddPasskey(w http.ResponseWriter, r *http.Request, u store.User, name string) (store.Passkey, error) {
	if a.webauthn == nil {
		return store.Passkey{}, ErrNoPasskeys
	}
	c, err := a.takeCeremony(w, r, "register")
	if err != nil || c.UserID != u.ID {
		return store.Passkey{}, errors.New("the request expired; try again")
	}
	pu, err := a.passkeyUser(r.Context(), u)
	if err != nil {
		return store.Passkey{}, err
	}
	cred, err := a.webauthn.FinishRegistration(pu, c.Session, r)
	if err != nil {
		return store.Passkey{}, err
	}
	data, err := json.Marshal(cred)
	if err != nil {
		return store.Passkey{}, err
	}
	return a.store.AddPasskey(r.Context(), u.ID, base64.RawURLEncoding.EncodeToString(cred.ID), data, name)
}
