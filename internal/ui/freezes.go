// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package ui

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/pipozzz/goliash/internal/auth"
	"github.com/pipozzz/goliash/internal/store"
)

// FreezeView is a change freeze on the settings page.
type FreezeView struct {
	ID, From, To, Note, By string
	Active                 bool
}

// freezeText says a change freeze for a banner or a pull request: "until 3 Jan 08:00 UTC: Christmas".
func freezeText(f store.Freeze) string {
	s := "until " + f.EndsAt.UTC().Format("2 Jan 15:04 MST")
	if f.Note != "" {
		s += ": " + f.Note
	}
	return s
}

// activeFreeze is the change freeze the workspace is in now, as words; "" outside one.
func (s *Server) activeFreeze(ctx context.Context, sc store.Scope) string {
	f, ok, err := s.store.ActiveFreeze(ctx, sc, time.Now())
	if err != nil || !ok {
		return ""
	}
	return freezeText(f)
}

// freezeNote is what a pull request opened during a change freeze says first; "" outside one.
func (s *Server) freezeNote(ctx context.Context, sc store.Scope) string {
	if f := s.activeFreeze(ctx, sc); f != "" {
		return "> ❄️ Opened during a change freeze (" + f + ").\n\n"
	}
	return ""
}

// freezeViews lists the workspace's change freezes that have not ended.
func (s *Server) freezeViews(ctx context.Context, sc store.Scope) ([]FreezeView, error) {
	now := time.Now()
	list, err := s.store.ListFreezes(ctx, sc, now)
	if err != nil {
		return nil, err
	}
	var out []FreezeView
	for _, f := range list {
		out = append(out, FreezeView{
			ID: f.ID, From: f.StartsAt.Format("2 Jan 2006 15:04 MST"), To: f.EndsAt.Format("2 Jan 2006 15:04 MST"),
			Note: f.Note, By: f.CreatedBy, Active: !f.StartsAt.After(now),
		})
	}
	return out, nil
}

// addFreeze records a change freeze from the settings page: from and to as the browser's date and time fields
// send them, in UTC.
func (s *Server) addFreeze(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	const page = "/settings#freezes"
	parse := func(v string) (time.Time, bool) {
		t, err := time.Parse("2006-01-02T15:04", strings.TrimSpace(v))
		return t, err == nil
	}
	from, ok1 := parse(r.FormValue("from"))
	to, ok2 := parse(r.FormValue("to"))
	if !ok1 || !ok2 || !to.After(from) {
		return back(w, r, page, "error", "Give the freeze a start and an end after it, in UTC.")
	}
	if to.Sub(from) > 90*24*time.Hour {
		return back(w, r, page, "error", "A change freeze lasts 90 days at most.")
	}
	note := strings.TrimSpace(r.FormValue("note"))
	if len(note) > 200 {
		note = note[:200]
	}
	if _, err := s.store.AddFreeze(r.Context(), store.Freeze{Scope: p.Scope, StartsAt: from, EndsAt: to, Note: note, CreatedBy: p.User.Email}); err != nil {
		return err
	}
	s.audit(r.Context(), p, "workspace.freeze", "from", from.Format(time.RFC3339), "to", to.Format(time.RFC3339), "note", note)
	return back(w, r, page, "notice", "Change freeze added.")
}

// deleteFreeze ends a change freeze, or drops one that has not started.
func (s *Server) deleteFreeze(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	if err := s.store.DeleteFreeze(r.Context(), p.Scope, r.PathValue("id")); err != nil {
		return back(w, r, "/settings#freezes", "error", "No such change freeze.")
	}
	s.audit(r.Context(), p, "workspace.freeze_deleted", "id", r.PathValue("id"))
	return back(w, r, "/settings#freezes", "notice", "Change freeze removed.")
}
