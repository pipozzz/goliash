// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package ui

import (
	"net/http"

	"github.com/pipozzz/goliash/internal/auth"
	"github.com/pipozzz/goliash/internal/versions"
)

// StatusView is Goliash's own health for the workspace: can its numbers be trusted right now?
type StatusView struct {
	Base
	versions.Health
}

func (s *Server) status(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	h, err := versions.LoadHealth(r.Context(), s.store, s.checker, p.Scope, s.version, s.updateCheck)
	if err != nil {
		return err
	}
	v := StatusView{Base: withFlash(s.base(r.Context(), p, "status", "Status"), r), Health: h}
	return render(w, r, StatusPage(v))
}
