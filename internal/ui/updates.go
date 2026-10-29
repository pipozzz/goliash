// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package ui

import (
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/pipozzz/goliash/internal/auth"
	"github.com/pipozzz/goliash/internal/notifier"
	"github.com/pipozzz/goliash/internal/store"
	"github.com/pipozzz/goliash/internal/tickets"
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
	Overdue      int    // upgrades past the workspace's remediation deadline, before the urgency filter
	OnlyOverdue  bool   // ?due=over: only those
	Kinds        []kind // where the upgrades run, for the filter's buttons
	Calendar     bool   // ?view=calendar: the upgrades with a date, by month
	Months       []CalendarMonth
}

// CalendarMonth is one month of the calendar: the upgrades whose end of life or deadline falls in it.
type CalendarMonth struct {
	Label string // "November 2026", or "Past" for what is overdue
	Past  bool
	Items []CalendarItem
}

// CalendarItem is one dated upgrade: when, and what ends or is due then.
type CalendarItem struct {
	Date time.Time
	What string // "end of life" or "deadline: exploited vulnerability"
	UpdateItem
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
	SecurityFix                          string    // what the releases up to Target say they fix
	Breaking                             []string  // what the releases up to Target say breaks: "2.0.0: Removed …"
	Due                                  time.Time // the remediation deadline, zero without one
	DueFor                               string    // for which reason: "exploited vulnerability"
	DaysLeft                             int       // until Due: negative once past
	Watched                              []string  // the security watches covering the service
	EOLPassed                            bool
	On                                   string // the targets running Running, when others run newer versions
	Since                                time.Time
	Level                                int
	LevelClass                           string
	LevelLabel                           string
	Skip                                 string // the version after Target: "skip this version" holds until a newer one
	Exploited                            []string
	Exposed                              []string // how the running version can be reached from outside
	Maintenance                          []string // the patches the platform has waiting for it
	Critical                             int
	Vulns                                versions.VulnDelta // what the upgrade does to known vulnerabilities, when known
	Brief                                string             // what to ask Goli for a briefing on this upgrade; empty without Goli
	PRs                                  []PromotePR        // Argo CD applications the upgrade can be opened in as a pull request
	ServiceID, EnvID                     string
	Kinds                                []string     // where it runs (lambda, ecs …), for icons and the filter
	What                                 string       // what it is when its name does not say: "Node.js runtime"
	Same                                 string       // the upgrade it shares with other rows (same image, versions, environment), for folding them
	Ticket                               *TicketView  // the open ticket filed for it, if any
	TicketChannels                       []ChannelOpt // trackers a ticket can be filed in by hand
}

// TicketView is a ticket filed in an issue tracker.
type TicketView struct{ Key, URL string }

// ChannelOpt is a channel to pick.
type ChannelOpt struct{ ID, Name string }

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
	_ = cw.Write([]string{"service", "application", "owner", "environment", "running", "on", "target", "urgency", "jump", "behind", "since", "end_of_life", "eol_passed", "exploited", "critical", "security_fix", "breaking", "watches", "due", "days_left", "release_notes", "exposed", "maintenance"})
	for _, it := range v.Items {
		exploited := strings.Join(it.Exploited, " ")
		if q != "" && !match(it.Service, it.App, it.Owner, it.Env, it.Running, it.On, it.Target, it.LevelLabel, it.Jump, it.EOL, exploited, it.SecurityFix, strings.Join(it.Watched, " "), strings.Join(it.Breaking, " ")) {
			continue
		}
		since, due, left := "", "", ""
		if !it.Since.IsZero() {
			since = it.Since.UTC().Format(time.DateOnly)
		}
		if !it.Due.IsZero() {
			due, left = it.Due.Format(time.DateOnly), strconv.Itoa(it.DaysLeft)
		}
		_ = cw.Write([]string{
			it.Service, it.App, it.Owner, it.Env, it.Running, it.On, it.Target, it.LevelLabel, it.Jump, it.Behind, since,
			it.EOL, strconv.FormatBool(it.EOLPassed), exploited, strconv.Itoa(it.Critical), it.SecurityFix, strings.Join(it.Breaking, " | "), strings.Join(it.Watched, " "), due, left, it.TargetURL, strings.Join(it.Exposed, "; "), strings.Join(it.Maintenance, "; "),
		})
	}
	cw.Flush()
	return cw.Error()
}

// dueText says where an upgrade stands against its remediation deadline: "overdue 5 days", "due today",
// "due in 3 days", "due 30 Oct".
func dueText(it UpdateItem) string {
	switch d := it.DaysLeft; {
	case d < 0:
		return "overdue " + plural(-d, "day", "days")
	case d == 0:
		return "due today"
	case d <= 7:
		return "due in " + plural(d, "day", "days")
	}
	return "due " + it.Due.Format("2 Jan")
}

// dueClass colours the deadline: past it red, within a week amber, else plain.
func dueClass(it UpdateItem) string {
	switch {
	case it.DaysLeft < 0:
		return "eol"
	case it.DaysLeft <= 7:
		return "warn"
	}
	return "neutral"
}

// breakingShown bounds the notes' lines an upgrade shows; the CSV and the checklist have them all.
const breakingShown = 3

// breakingBadge names the releases whose notes say something breaks: "breaking in 2.0.0", "breaking in 3 releases".
func breakingBadge(lines []string) string {
	if len(lines) == 1 {
		v, _, _ := strings.Cut(lines[0], ": ")
		return "breaking in " + v
	}
	return "breaking in " + itoa(len(lines)) + " releases"
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
	v := UpdatesView{Base: withFlash(s.base(ctx, p, "updates", "Updates"), r), Team: q.Get("team"), Env: q.Get("env"), Level: -1, OnlyOverdue: q.Get("due") == "over", Calendar: q.Get("view") == "calendar"}
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
	open, trackers := s.ticketsOf(ctx, p)
	apps, err := versions.GitOpsApps(ctx, s.store, p.Scope, o)
	if err != nil {
		return UpdatesView{}, err
	}
	now := time.Now()
	kinds := serviceKinds(o)
	for _, u := range versions.LoadUpdates(ctx, s.store, p.Scope, o, acks, now) {
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
			Exploited: u.Exploited, Exposed: u.Exposed, Maintenance: u.Maintenance, Critical: u.Critical, Vulns: u.Vulns, SecurityFix: u.SecurityFix, Breaking: u.Breaking, Watched: watched[u.Service.ID],
			Due: u.Due, DueFor: u.DueFor, ServiceID: u.Service.ID, EnvID: u.Environment.ID, Ticket: open[ticketKey(u.Service.ID, u.Environment.ID, u.App)],
			TicketChannels: trackers,
		}
		if !u.Due.IsZero() {
			it.DaysLeft = u.DaysLeft(now)
		}
		it.Kinds, it.What = kinds[u.Service.ID], rowWhat(o.Refs[u.Service.ID].Repo)
		if repo := o.Refs[u.Service.ID].Repo; repo != "" {
			it.Same = strings.Join([]string{repo, u.Running, u.Target, u.Environment.Name, strconv.Itoa(u.Urgency)}, "|")
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
		overdue := u.Overdue(now)
		if overdue {
			v.Overdue++
		}
		if v.Level >= 0 && u.Urgency != v.Level || v.OnlyOverdue && !overdue {
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
	// A key shared by one row is no fold; the kinds present become the filter's buttons.
	same := map[string]int{}
	rows := make([]MatrixRow, 0, len(v.Items))
	for _, it := range v.Items {
		same[it.Same]++
		rows = append(rows, MatrixRow{Kinds: it.Kinds})
	}
	for i := range v.Items {
		if same[v.Items[i].Same] < 2 {
			v.Items[i].Same = ""
		}
	}
	v.Kinds = kindOptions(rows)
	var more []CalendarItem
	if v.Calendar {
		more = s.calendarEOL(ctx, o, v)
	}
	v.Months = calendarMonths(v.Items, more, time.Now())
	return v, nil
}

// calendarHorizon is how far ahead the calendar lists ends of life of what runs.
const calendarHorizon = 18 * 31 * 24 * time.Hour

// calendarEOL are the ends of life of every version that runs, beyond the upgrades: the release cycle's date from
// endoflife.date, up to calendarHorizon ahead, for the calendar.
func (s *Server) calendarEOL(ctx context.Context, o versions.Overview, v UpdatesView) []CalendarItem {
	if s.checker == nil {
		return nil
	}
	kinds := serviceKinds(o)
	limit := time.Now().Add(calendarHorizon)
	var out []CalendarItem
	for _, row := range o.Matrix.Rows {
		svc := row.Service
		if v.Team != "" && svc.Owner != v.Team {
			continue
		}
		repo := o.Refs[svc.ID].Repo
		for i, c := range row.Cells {
			env := o.Matrix.Environments[i].Name
			if v.Env != "" && env != v.Env {
				continue
			}
			for _, ver := range c.Versions {
				date, cycle, ok := s.checker.EOLOf(ctx, svc, repo, ver.Tag)
				if !ok || date.After(limit) {
					continue
				}
				out = append(out, CalendarItem{Date: date, What: "end of life of " + cycle, UpdateItem: UpdateItem{
					Service: svc.Name, ServiceURL: serviceURL(svc.Name), Owner: svc.Owner, Env: env, Running: ver.Version(),
					Kinds: kinds[svc.ID], What: rowWhat(repo),
				}})
			}
		}
	}
	return out
}

// calendarMonths puts the upgrades with a date (an end of life, a remediation deadline) in order, by month, what is
// past first.
func calendarMonths(items []UpdateItem, more []CalendarItem, now time.Time) []CalendarMonth {
	dated := append([]CalendarItem(nil), more...)
	listed := map[string]bool{}
	for _, it := range items {
		if it.EOL != "" {
			listed[it.Service+"|"+it.Env] = true
		}
	}
	dated = slices.DeleteFunc(dated, func(c CalendarItem) bool { return listed[c.Service+"|"+c.Env] }) // the upgrade's own line has it
	for _, it := range items {
		if !it.Due.IsZero() {
			dated = append(dated, CalendarItem{Date: it.Due, What: "deadline: " + it.DueFor, UpdateItem: it})
		}
		if it.EOL != "" {
			if d, err := time.Parse(time.DateOnly, it.EOL); err == nil {
				dated = append(dated, CalendarItem{Date: d, What: "end of life", UpdateItem: it})
			}
		}
	}
	sort.SliceStable(dated, func(i, j int) bool { return dated[i].Date.Before(dated[j].Date) })
	var out []CalendarMonth
	today := now.Truncate(24 * time.Hour)
	for _, d := range dated {
		label, past := d.Date.Format("January 2006"), d.Date.Before(today)
		if past {
			label = "Past"
		}
		if len(out) == 0 || out[len(out)-1].Label != label {
			out = append(out, CalendarMonth{Label: label, Past: past})
		}
		out[len(out)-1].Items = append(out[len(out)-1].Items, d)
	}
	return out
}

// updateSearch is what the filter matches a row by: what it shows, its kinds (type:lambda) and what it is.
func updateSearch(it UpdateItem) string {
	parts := []string{it.Service, it.App, it.Owner, it.Env, it.On, it.Running, it.Target, it.Jump, it.LevelLabel, it.What, kindTokens(it.Kinds)}
	parts = append(parts, it.Exploited...)
	parts = append(parts, it.Exposed...)
	if it.EOL != "" {
		parts = append(parts, "end of life", it.EOL)
	}
	return strings.ToLower(strings.Join(parts, " "))
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
	if len(it.Breaking) > 0 {
		why = append(why, "breaking: "+strings.Join(it.Breaking, "; "))
	}
	if !it.Due.IsZero() {
		why = append(why, dueText(it))
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
	if v.OnlyOverdue {
		q.Set("due", "over")
	}
	if v.Calendar {
		q.Set("view", "calendar")
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

// overdueToggle is the due filter a click on the tile sets: on, or off again.
func overdueToggle(v UpdatesView) string {
	if v.OnlyOverdue {
		return ""
	}
	return "over"
}

// allDue is the view without the due filter, for the All tile.
func allDue(v UpdatesView) UpdatesView {
	v.OnlyOverdue = false
	return v
}

// exposedTitle says how something can be reached from outside.
func exposedTitle(ways []string) string { return "Reachable from outside: " + strings.Join(ways, "; ") }

// maintenanceTitle says which patches the platform has waiting.
func maintenanceTitle(m []string) string { return "Waiting at the platform: " + strings.Join(m, "; ") }

func ticketKey(serviceID, envID, app string) string { return serviceID + "|" + envID + "|" + app }

// ticketsOf is the workspace's open tickets by upgrade, and the trackers a member may file one in by hand.
func (s *Server) ticketsOf(ctx context.Context, p auth.Principal) (map[string]*TicketView, []ChannelOpt) {
	open := map[string]*TicketView{}
	if ts, err := s.store.ListOpenTickets(ctx, p.Scope); err == nil {
		for _, t := range ts {
			open[ticketKey(t.ServiceID, t.EnvironmentID, t.App)] = &TicketView{Key: t.ExternalKey, URL: t.URL}
		}
	}
	var trackers []ChannelOpt
	if p.Can(store.RoleMember) {
		if chans, err := s.store.ListChannels(ctx, p.Scope); err == nil {
			for _, c := range chans {
				if tickets.Is(c.Type) {
					trackers = append(trackers, ChannelOpt{ID: c.ID, Name: c.Name})
				}
			}
		}
	}
	return open, trackers
}

func (s *Server) fileTicket(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	ret := "/updates"
	if q := r.FormValue("return"); q == "/updates" || strings.HasPrefix(q, "/updates?") {
		ret = q
	}
	t, err := s.notify.FileTicket(r.Context(), p.Scope, r.FormValue("channel"), r.FormValue("service"), r.FormValue("env"), r.FormValue("app"))
	switch {
	case errors.Is(err, notifier.ErrNoUpgrade):
		return back(w, r, ret, "error", "There is nothing to upgrade there any more.")
	case err != nil:
		return back(w, r, ret, "error", "The tracker refused the ticket: "+err.Error())
	}
	s.audit(r.Context(), p, "ticket.file", "ticket", t.ExternalKey, "url", t.URL)
	return back(w, r, ret, "notice", "Ticket "+t.ExternalKey+" filed: "+t.URL)
}
