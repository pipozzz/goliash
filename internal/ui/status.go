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
	// Goli is optional: whether it is on never makes the status worse.
	g := versions.HealthCheck{
		Title: "Goli, the assistant", State: "off", Link: "https://goliash.dev/guide/goli/", LinkText: "How",
		Summary: "Off. Set GOLIASH_AI_API_KEY on the server to an Anthropic API key to ask questions in plain words.",
	}
	if s.goliOn() {
		g.State, g.Summary, g.Link, g.LinkText = "ok", "On, answering with "+s.goli.Model()+" ("+s.goli.Via()+").", "", ""
	}
	h.Checks = append(h.Checks, g)
	v := StatusView{Base: withFlash(s.base(r.Context(), p, "status", "Status"), r), Health: h}
	return render(w, r, StatusPage(v))
}
