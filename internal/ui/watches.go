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
	"github.com/pipozzz/goliash/internal/versions"
)

// WatchView is a security watch on the Security page: what it looks for, why, until when, and what it covers.
type WatchView struct {
	ID, Term, Note, Until, By string
	Matches                   []versions.WatchMatch
}

// watchViews lists the workspace's active security watches with the services each covers.
func (s *Server) watchViews(ctx context.Context, sc store.Scope) ([]WatchView, error) {
	now := time.Now()
	ws, err := s.store.ListWatches(ctx, sc, now)
	if err != nil || len(ws) == 0 {
		return nil, err
	}
	o, err := versions.LoadOverview(ctx, s.store, sc)
	if err != nil {
		return nil, err
	}
	terms := make([]string, len(ws))
	for i, w := range ws {
		terms[i] = w.Term
	}
	matches, err := versions.WatchMatches(ctx, s.store, sc, o, terms)
	if err != nil {
		return nil, err
	}
	out := make([]WatchView, 0, len(ws))
	for _, w := range ws {
		v := WatchView{ID: w.ID, Term: w.Term, Note: w.Note, By: w.CreatedBy, Matches: matches[w.Term]}
		if !w.Until.IsZero() {
			v.Until = w.Until.Format("2 Jan 2006")
		}
		out = append(out, v)
	}
	return out, nil
}

// addWatch starts a security watch: "php", why, and for how many days (0: until removed).
func (s *Server) addWatch(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	const page = "/security#watches"
	term := strings.TrimSpace(r.FormValue("term"))
	if term == "" || len(term) > 100 {
		return back(w, r, page, "error", "Name what to watch: a product, image or package, like php or openssl.")
	}
	note := strings.TrimSpace(r.FormValue("note"))
	if len(note) > 200 {
		note = note[:200]
	}
	watch := store.SecurityWatch{Scope: p.Scope, Term: term, Note: note, CreatedBy: p.User.Email}
	switch days := r.FormValue("days"); days {
	case "", "0":
	case "7", "14", "30", "90":
		n := map[string]int{"7": 7, "14": 14, "30": 30, "90": 90}[days]
		watch.Until = time.Now().Add(time.Duration(n) * 24 * time.Hour)
	default:
		return back(w, r, page, "error", "Pick how long to watch.")
	}
	if _, err := s.store.AddWatch(r.Context(), watch); err != nil {
		return err
	}
	s.audit(r.Context(), p, "workspace.watch", "term", term, "note", note)
	return back(w, r, page, "notice", "Watching “"+term+"”: a new release of what it covers is announced at once.")
}

// deleteWatch ends a security watch.
func (s *Server) deleteWatch(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	if err := s.store.DeleteWatch(r.Context(), p.Scope, r.PathValue("id")); err != nil {
		return err
	}
	s.audit(r.Context(), p, "workspace.unwatch", "id", r.PathValue("id"))
	return back(w, r, "/security#watches", "notice", "Watch removed.")
}
