// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package ui

import (
	"context"
	"net/http"
	"slices"
	"strconv"
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

// deadlineDays are the remediation deadlines an admin may pick, in days; 0 sets none.
var deadlineDays = []int{0, 3, 7, 14, 30, 60, 90, 180}

// setDeadlines sets the workspace's remediation deadlines.
func (s *Server) setDeadlines(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	const page = "/security#deadlines"
	days := func(name string) (int, bool) {
		n, err := strconv.Atoi(r.FormValue(name))
		return n, err == nil && slices.Contains(deadlineDays, n)
	}
	exploited, ok1 := days("exploited")
	security, ok2 := days("security")
	eol, ok3 := days("eol")
	if !ok1 || !ok2 || !ok3 {
		return back(w, r, page, "error", "Pick the days from the lists.")
	}
	rem := store.Remediation{Exploited: exploited, Security: security, EOL: eol}
	if err := s.store.SetRemediation(r.Context(), p.Scope, rem); err != nil {
		return err
	}
	s.audit(r.Context(), p, "workspace.deadlines", "exploited", strconv.Itoa(exploited), "security", strconv.Itoa(security), "eol", strconv.Itoa(eol))
	if !rem.Any() {
		return back(w, r, page, "notice", "No remediation deadlines.")
	}
	return back(w, r, page, "notice", "Remediation deadlines saved: Updates marks what is due and what is past.")
}

// dueCounts counts the open upgrades past their remediation deadline and those due within a week.
func (s *Server) dueCounts(ctx context.Context, sc store.Scope) (overdue, soon int, err error) {
	o, err := versions.LoadOverview(ctx, s.store, sc)
	if err != nil {
		return 0, 0, err
	}
	acks, err := s.store.ListAcks(ctx, sc)
	if err != nil {
		return 0, 0, err
	}
	now := time.Now()
	for _, u := range versions.LoadUpdates(ctx, s.store, sc, o, acks, now) {
		switch {
		case u.Acked || u.Due.IsZero():
		case u.Overdue(now):
			overdue++
		case u.DaysLeft(now) <= 7:
			soon++
		}
	}
	return overdue, soon, nil
}

// deadlineLabel names a deadline choice: "7 days", "none".
func deadlineLabel(days int) string {
	if days == 0 {
		return "none"
	}
	return plural(days, "day", "days")
}
