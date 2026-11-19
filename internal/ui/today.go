// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package ui

import (
	"net/http"
	"time"

	"github.com/pipozzz/goliash/internal/auth"
	"github.com/pipozzz/goliash/internal/notifier"
	"github.com/pipozzz/goliash/internal/versions"
)

// todayShown bounds the upgrades the Today page lists; the rest are a click away on Updates.
const todayShown = 8

// TodayView is the Today page: what to do first, in one list, from what the other pages know.
type TodayView struct {
	Base
	Urgent      []UpdateItem            // the upgrades that cannot wait, most urgent first, at most todayShown
	UrgentTotal int                     // all of them
	Later       int                     // the other open upgrades
	Rollouts    []versions.RolloutIssue // what is broken now: rollouts that do not finish
	Brief       notifier.Brief          // what changed since yesterday
	At          time.Time
}

// urgentToday says an upgrade belongs on the Today page: an exploited vulnerability, a release that fixes
// vulnerabilities, a release cycle past its end of life, or anything past the workspace's deadline.
func urgentToday(it UpdateItem) bool {
	return it.Level <= 2 || (!it.Due.IsZero() && it.DaysLeft < 0)
}

// today shows what to do first.
func (s *Server) today(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	ctx := r.Context()
	// Straight from signing in: people who chose a home view go there.
	if r.URL.Query().Get("home") == "1" {
		if viewURL(s.homeViewURL(ctx, p)) != "" || (!s.auth.Demo() && p.User.HomeView != "") {
			http.Redirect(w, r, "/", http.StatusFound)
			return nil
		}
	}
	plain := r.Clone(ctx)
	plain.URL.RawQuery = "" // the whole list, whatever the address says
	uv, err := s.updatesView(plain, p)
	if err != nil {
		return err
	}
	v := TodayView{Base: withFlash(s.base(ctx, p, "today", "Today"), r), At: time.Now()}
	for _, it := range uv.Items {
		if !urgentToday(it) {
			v.Later++
			continue
		}
		v.UrgentTotal++
		if len(v.Urgent) < todayShown {
			v.Urgent = append(v.Urgent, it)
		}
	}
	o, err := versions.LoadOverview(ctx, s.store, p.Scope)
	if err != nil {
		return err
	}
	if v.Rollouts, err = versions.RolloutIssues(ctx, s.store, p.Scope, o, time.Now()); err != nil {
		return err
	}
	if v.Brief, err = notifier.LoadBrief(ctx, s.store, p.Scope, v.At); err != nil {
		return err
	}
	return render(w, r, TodayPage(v))
}
