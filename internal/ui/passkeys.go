// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package ui

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/pipozzz/goliash/internal/auth"
	"github.com/pipozzz/goliash/internal/store"
)

// PasskeyView is one passkey on the account page.
type PasskeyView struct {
	ID        string
	Name      string
	CreatedAt time.Time
	LastUsed  time.Time
}

func jsonError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// passkeyOptions starts adding a passkey (JSON for the browser).
func (s *Server) passkeyOptions(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	if err := s.auth.PasskeyRegistrationOptions(w, r, p.User); err != nil {
		if errors.Is(err, auth.ErrNoPasskeys) {
			jsonError(w, http.StatusNotFound, err.Error())
			return nil
		}
		return err
	}
	return nil
}

// addPasskey stores the passkey the browser created.
func (s *Server) addPasskey(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	name := strings.TrimSpace(r.URL.Query().Get("name"))
	if name == "" {
		name = device(r.UserAgent())
	}
	if len(name) > 60 {
		name = name[:60]
	}
	k, err := s.auth.AddPasskey(w, r, p.User, name)
	if errors.Is(err, store.ErrExists) {
		jsonError(w, http.StatusConflict, "This passkey is added already.")
		return nil
	}
	if err != nil {
		jsonError(w, http.StatusBadRequest, "The passkey could not be added: "+err.Error())
		return nil
	}
	s.audit(r.Context(), p, "user.passkey_add", "passkey", k.Name)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"redirect": "/account?passkey=added"})
	return nil
}

func (s *Server) renamePasskey(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" || len(name) > 60 {
		return back(w, r, "/account", "error", "Name the passkey with 1 to 60 characters.")
	}
	if err := s.store.RenamePasskey(r.Context(), p.User.ID, r.PathValue("id"), name); err != nil {
		return back(w, r, "/account", "error", "Unknown passkey.")
	}
	return back(w, r, "/account", "notice", "Passkey renamed.")
}

func (s *Server) deletePasskey(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	if err := s.store.DeletePasskey(r.Context(), p.User.ID, r.PathValue("id")); err != nil {
		return back(w, r, "/account", "error", "Unknown passkey.")
	}
	s.audit(r.Context(), p, "user.passkey_delete")
	return back(w, r, "/account", "notice", "Passkey removed. It no longer signs you in here; remove it from your device too.")
}
