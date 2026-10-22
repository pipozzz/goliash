// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package ui

import (
	"context"
	"encoding/csv"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/pipozzz/goliash/internal/auth"
	"github.com/pipozzz/goliash/internal/store"
	"github.com/pipozzz/goliash/internal/versions"
)

// UpdatesView is the to-do list of upgrades.
type UpdatesView struct {
	Base
	Views        ViewTabs
	Items, Acked []UpdateItem
	Bulk         bool // some upgrade can be ticked and opened as a pull request with others
	Levels       []UpdateLevel
	Level        int // the urgency shown; -1 for all
	Teams, Envs  []string
	Team, Env    string
	Markdown     string // the list as a markdown checklist, to copy into a ticket
	Total        int    // before filters
	Watched      []WatchedItem
}

// WatchedItem is a service watched for its releases that runs nowhere Goliash knows.
type WatchedItem struct {
	Service, Upstream, Latest, URL string
	Published                      time.Time
}

// UpdateLevel is one urgency with its count, for the tiles.
type UpdateLevel struct {
	Level        int
	Label, Class string
	Count        int
}

// UpdateItem is one upgrade.
type UpdateItem struct {
	Service, ServiceURL, App, Owner, Env string
	Running, Target, TargetURL           string
	Jump, Behind, EOL                    string
	SecurityFix                          string   // what the releases up to Target say they fix
	Watched                              []string // the security watches covering the service
	EOLPassed                            bool
	On                                   string // the targets running Running, when others run newer versions
	Since                                time.Time
	Level                                int
	LevelClass                           string
	LevelLabel                           string
	Skip                                 string // the version after Target: "skip this version" holds until a newer one
	Exploited                            []string
	Critical                             int
	Vulns                                versions.VulnDelta // what the upgrade does to known vulnerabilities, when known
	Brief                                string             // what to ask Goli for a briefing on this upgrade; empty without Goli
	PRs                                  []PromotePR        // Argo CD applications the upgrade can be opened in as a pull request
}

var levelClasses = []string{"eol", "eol", "eol", "eol", "inconsistent", "upstream", "env", "neutral"}

func (s *Server) updates(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	v, err := s.updatesView(r, p)
	if err != nil {
		return err
	}
	return render(w, r, UpdatesPage(v))
}

// updatesCSV downloads the upgrades the page shows: its team, environment and level, and the filter's words (?q=).
func (s *Server) updatesCSV(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	v, err := s.updatesView(r, p)
	if err != nil {
		return err
	}
	q := r.URL.Query().Get("q")
	match := versions.Matcher(q)
	csvHeaders(w, "upgrades")
	cw := csv.NewWriter(w)
	_ = cw.Write([]string{"service", "application", "owner", "environment", "running", "on", "target", "urgency", "jump", "behind", "since", "end_of_life", "eol_passed", "exploited", "critical", "security_fix", "watches", "release_notes"})
	for _, it := range v.Items {
		exploited := strings.Join(it.Exploited, " ")
		if q != "" && !match(it.Service, it.App, it.Owner, it.Env, it.Running, it.On, it.Target, it.LevelLabel, it.Jump, it.EOL, exploited, it.SecurityFix, strings.Join(it.Watched, " ")) {
			continue
		}
		since := ""
		if !it.Since.IsZero() {
			since = it.Since.UTC().Format(time.DateOnly)
		}
		_ = cw.Write([]string{
			it.Service, it.App, it.Owner, it.Env, it.Running, it.On, it.Target, it.LevelLabel, it.Jump, it.Behind, since,
			it.EOL, strconv.FormatBool(it.EOLPassed), exploited, strconv.Itoa(it.Critical), it.SecurityFix, strings.Join(it.Watched, " "), it.TargetURL,
		})
	}
	cw.Flush()
	return cw.Error()
}

// updatesCSVHref downloads what the page shows; the filter box adds its words as it changes.
func updatesCSVHref(v UpdatesView) string {
	return strings.Replace(updatesURL(v, "", ""), "/updates", "/updates.csv", 1)
}

// updatesView is the Updates page for the request's team, environment and level.
func (s *Server) updatesView(r *http.Request, p auth.Principal) (UpdatesView, error) {
	ctx := r.Context()
	o, err := versions.LoadOverview(ctx, s.store, p.Scope)
	if err != nil {
		return UpdatesView{}, err
	}
	acks, err := s.store.ListAcks(ctx, p.Scope)
	if err != nil {
		return UpdatesView{}, err
	}
	watched := map[string][]string{} // service ID -> the security watches covering it
	if ws, err := s.store.ListWatches(ctx, p.Scope, time.Now()); err == nil && len(ws) > 0 {
		terms := make([]string, len(ws))
		for i, w := range ws {
			terms[i] = w.Term
		}
		if matches, err := versions.WatchMatches(ctx, s.store, p.Scope, o, terms); err == nil {
			for term, ms := range matches {
				for _, m := range ms {
					watched[m.ServiceID] = append(watched[m.ServiceID], term)
				}
			}
		}
	}
	q := r.URL.Query()
	v := UpdatesView{Base: withFlash(s.base(ctx, p, "updates", "Updates"), r), Team: q.Get("team"), Env: q.Get("env"), Level: -1}
	v.Views = s.viewTabs(ctx, r, p, "/updates")
	if l := q.Get("level"); l != "" {
		for i := range versions.UrgencyLabels {
			if itoa(i) == l {
				v.Level = i
			}
		}
	}
	teams, envs := map[string]bool{}, map[string]bool{}
	for i, label := range versions.UrgencyLabels {
		v.Levels = append(v.Levels, UpdateLevel{Level: i, Label: label, Class: levelClasses[i]})
	}
	var md strings.Builder
	apps, err := versions.GitOpsApps(ctx, s.store, p.Scope, o)
	if err != nil {
		return UpdatesView{}, err
	}
	for _, u := range versions.LoadUpdates(ctx, s.store, p.Scope, o, acks, time.Now()) {
		if u.Service.Owner != "" {
			teams[u.Service.Owner] = true
		}
		envs[u.Environment.Name] = true
		if (v.Team != "" && u.Service.Owner != v.Team) || (v.Env != "" && u.Environment.Name != v.Env) {
			continue
		}
		it := UpdateItem{
			Service: u.Service.Name, ServiceURL: serviceURL(u.Service.Name), App: u.App, Owner: u.Service.Owner,
			Env: u.Environment.Name, Running: u.Running, Target: u.Target, TargetURL: u.TargetURL, Jump: string(u.Jump),
			Behind: u.Behind, EOL: u.EOL, EOLPassed: u.EOLPassed, Since: u.Since, Level: u.Urgency, On: strings.Join(u.On, ", "),
			LevelLabel: versions.UrgencyLabels[u.Urgency], LevelClass: levelClasses[u.Urgency], Skip: nextVersion(u.Target),
			Exploited: u.Exploited, Critical: u.Critical, Vulns: u.Vulns, SecurityFix: u.SecurityFix, Watched: watched[u.Service.ID],
		}
		if p.Can(store.RoleMember) || s.demoPulls() {
			it.PRs = s.upgradePRs(ctx, p.Scope, o, apps, u.Service.ID, u.Environment.Name, u.Running, u.Target)
			for _, pr := range it.PRs {
				v.Bulk = v.Bulk || pr.URL == "" && p.Can(store.RoleMember) && !s.demoPulls()
			}
		}
		if s.goliOn() && u.Running != "" && u.Target != "" {
			it.Brief = fmt.Sprintf("Brief me on upgrading %s in %s from %s to %s: what breaks or needs migrating, "+
				"what it fixes (security first), and what to check before and after.", u.Service.Name, u.Environment.Name, u.Running, u.Target)
		}
		if u.Acked {
			v.Acked = append(v.Acked, it)
			continue
		}
		v.Total++
		v.Levels[u.Urgency].Count++
		if v.Level >= 0 && u.Urgency != v.Level {
			continue
		}
		v.Items = append(v.Items, it)
		md.WriteString(updateMarkdown(it))
	}
	if lv := &v.Levels[versions.UrgencyExploited]; lv.Count != 1 {
		lv.Label = "exploited vulnerabilities"
	}
	v.Teams, v.Envs = sortedSet(teams), sortedSet(envs)
	if v.Watched, err = s.watched(ctx, p.Scope, o); err != nil {
		return UpdatesView{}, err
	}
	v.Markdown = md.String()
	return v, nil
}

// updateMarkdown is one checklist line: "- [ ] postgres (auth) @ prod: 15.6 → 15.8 (end of life 2026-09-01)".
func updateMarkdown(it UpdateItem) string {
	var b strings.Builder
	b.WriteString("- [ ] " + it.Service)
	if it.App != "" {
		b.WriteString(" (" + it.App + ")")
	}
	b.WriteString(" @ " + it.Env + ": " + orDash(it.Running))
	if it.On != "" {
		b.WriteString(" on " + it.On)
	}
	if it.Target != "" {
		b.WriteString(" → " + it.Target)
	}
	var why []string
	if it.EOL != "" {
		if it.EOLPassed {
			why = append(why, "end of life since "+it.EOL)
		} else {
			why = append(why, "end of life on "+it.EOL)
		}
	}
	if it.Jump != "" {
		why = append(why, it.Jump)
	}
	if it.Behind != "" {
		why = append(why, "behind "+it.Behind)
	}
	if len(why) > 0 {
		b.WriteString(" (" + strings.Join(why, ", ") + ")")
	}
	if it.TargetURL != "" {
		b.WriteString(" — " + it.TargetURL)
	}
	return b.String() + "\n"
}

// nextVersion is the smallest version after v, its last number plus one ("26.8.0" ->
// "26.8.1"): an acknowledgement until it holds while nothing newer than v is out.
func nextVersion(v string) string {
	if _, ok := versions.ParseVersion(v); !ok {
		return ""
	}
	end := strings.IndexFunc(v, func(r rune) bool { return r == '-' || r == '+' || r == '_' })
	if end < 0 {
		end = len(v)
	}
	start := end
	for start > 0 && v[start-1] >= '0' && v[start-1] <= '9' {
		start--
	}
	if start == end {
		return ""
	}
	n, err := strconv.Atoi(v[start:end])
	if err != nil {
		return ""
	}
	return v[:start] + strconv.Itoa(n+1) + v[end:]
}

// updatesURL keeps the page's filters, changing one.
func updatesURL(v UpdatesView, key, value string) string {
	q := url.Values{}
	if v.Team != "" {
		q.Set("team", v.Team)
	}
	if v.Env != "" {
		q.Set("env", v.Env)
	}
	if v.Level >= 0 {
		q.Set("level", itoa(v.Level))
	}
	if value == "" {
		q.Del(key)
	} else {
		q.Set(key, value)
	}
	if len(q) == 0 {
		return "/updates"
	}
	return "/updates?" + q.Encode()
}

func sortedSet(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// watched lists the services with an upstream that run nowhere, with their newest release.
func (s *Server) watched(ctx context.Context, sc store.Scope, o versions.Overview) ([]WatchedItem, error) {
	running := map[string]bool{}
	for _, row := range o.Matrix.Rows {
		running[row.Service.ID] = true
	}
	services, err := s.store.ListServices(ctx, sc)
	if err != nil {
		return nil, err
	}
	var out []WatchedItem
	for _, svc := range services {
		if running[svc.ID] || svc.Upstream == "" {
			continue
		}
		it := WatchedItem{Service: svc.Name, Upstream: svc.Upstream}
		rels, err := s.store.ListReleases(ctx, sc, svc.ID)
		if err != nil {
			return nil, err
		}
		var newest *versions.Version
		for _, r := range rels {
			if v, ok := versions.ParseVersion(r.Version); ok && v.Pre == "" && (newest == nil || v.Compare(*newest) > 0) {
				newest = &v
				it.Latest, it.Published, it.URL = r.Version, r.PublishedAt, r.ChangelogURL
			}
		}
		out = append(out, it)
	}
	return out, nil
}
