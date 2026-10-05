// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package ui

import (
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/pipozzz/goliash/internal/notifier"
	"github.com/pipozzz/goliash/internal/store"
	"github.com/pipozzz/goliash/internal/versions"
)

// Base is what every signed-in page shows.
type Base struct {
	Title      string
	Page       string // nav key: matrix, inbox, events, agents, notifications, settings
	Email      string
	Role       string
	InboxCount int
	Notice     string
	Error      string
	CanMember  bool
	CanAdmin   bool
	OrgAdmin   bool // organization owner or admin: manages workspaces
	Workspace  string
	Workspaces []WorkspaceOption
}

// WorkspaceOption is one entry of the workspace switcher.
type WorkspaceOption struct {
	ID      string
	Name    string
	Current bool
}

// WorkspaceItem is one row of the workspaces page.
type WorkspaceItem struct {
	ID                         string
	Name, Slug                 string
	Targets, Services, Members int
	Current                    bool
}

// WorkspacesView is the workspaces page.
type WorkspacesView struct {
	Base
	Items []WorkspaceItem
}

// MatrixView is the home page.
type MatrixView struct {
	Base
	Grid    MatrixGrid
	Hygiene int    // image hygiene warnings
	At      string // "2006-01-02T15:04" (UTC) when looking back; empty for now
}

// MatrixGrid is the part of the matrix that reloads live.
type MatrixGrid struct {
	Envs     []EnvHeader
	Rows     []MatrixRow
	GroupBy  string        // app, team, status or none
	Groups   []MatrixGroup // Rows arranged by GroupBy; one unnamed group for none
	Unmapped int
	Targets  int
	Agents   int
	Updated  time.Time
	Steps    []Step // first-run checklist, while the matrix is empty
}

// MatrixGroup is a run of matrix rows under one heading.
type MatrixGroup struct {
	Name, Caption string
	Rows          []MatrixRow
	OK, Warn, Bad int // rows by health, for the group's bar
}

// healthBar is the group's stacked bar: widths of the ok, warn and bad parts out of w.
func (g MatrixGroup) healthBar(w float64) (ok, warn, bad float64) {
	n := float64(len(g.Rows))
	if n == 0 {
		return 0, 0, 0
	}
	return w * float64(g.OK) / n, w * float64(g.Warn) / n, w * float64(g.Bad) / n
}

// matrixGroupModes are the ways to arrange the matrix, application first. Namespaces
// are left out: one service usually runs in a different one per environment.
var matrixGroupModes = []struct{ Key, Label string }{
	{"app", "Application"}, {"team", "Team"}, {"status", "Status"}, {"none", "None"},
}

// matrixGroupBy returns a known grouping, application by default.
func matrixGroupBy(s string) string {
	for _, m := range matrixGroupModes {
		if m.Key == s {
			return s
		}
	}
	return "app"
}

// groupRows arranges matrix rows under headings, keeping their order within each.
func groupRows(rows []MatrixRow, by string) []MatrixGroup {
	if by == "none" {
		return []MatrixGroup{{Rows: rows}}
	}
	byKey := map[string][]MatrixRow{}
	sources := map[string]map[string]bool{}
	for _, r := range rows {
		var key string
		switch by {
		case "team":
			key = r.Owner
			if key == "" {
				key = "no owner"
			}
		case "status":
			key = r.Health
		default:
			key = r.App
			if key == "" {
				key = "other"
			}
			if sources[key] == nil {
				sources[key] = map[string]bool{}
			}
			sources[key][r.AppSource] = true
		}
		byKey[key] = append(byKey[key], r)
	}
	last := func(k string) bool { return k == "other" || k == "no owner" }
	var keys []string
	if by == "status" {
		for _, sc := range statusColumns {
			if len(byKey[sc.key]) > 0 {
				keys = append(keys, sc.key)
			}
		}
	} else {
		for k := range byKey {
			keys = append(keys, k)
		}
		sort.Slice(keys, func(i, j int) bool {
			if last(keys[i]) != last(keys[j]) {
				return last(keys[j])
			}
			return keys[i] < keys[j]
		})
	}
	out := make([]MatrixGroup, 0, len(keys))
	for _, k := range keys {
		g := MatrixGroup{Name: k, Rows: byKey[k]}
		for _, r := range g.Rows {
			switch r.Health {
			case "ok":
				g.OK++
			case "warn":
				g.Warn++
			default:
				g.Bad++
			}
		}
		if by == "status" {
			for _, sc := range statusColumns {
				if sc.key == k {
					g.Name = sc.label
				}
			}
		}
		if by == "app" && len(sources[k]) == 1 {
			for src := range sources[k] {
				g.Caption = appSourceLabel(src)
			}
		}
		out = append(out, g)
	}
	return out
}

// Step is one item of the first-run checklist.
type Step struct {
	Title    string
	Text     string
	Href     string
	Action   string
	Done     bool
	Optional bool
}

// EnvHeader is a matrix column.
type EnvHeader struct {
	Name    string
	Targets int
}

// MatrixRow is one service.
type MatrixRow struct {
	Service   string
	Owner     string
	App       string
	AppSource string
	Health    string // ok, warn or bad, from the row's drift
	URL       string
	Cells     []MatrixCell
	Latest    string
	LatestURL string
	Note      string // e.g. "17.0 outside pin", "checked by agent"
	NoteTitle string
}

// MatrixCell is one service in one environment.
type MatrixCell struct {
	Versions     []VersionView
	FromDeclared bool // nothing reports what runs; the versions come from Compose files
	Drifts       []DriftBadge
	StaleTitle   string // set when some of the data comes from targets that stopped reporting
}

// VersionView is one tag running in a cell.
type VersionView struct {
	Tag     string
	Running int
	Targets string
}

// DriftBadge is a short drift label with an explanation on hover.
type DriftBadge struct {
	Kind  string
	Label string
	Title string
	Since time.Time
}

func serviceURL(name string) string { return "/services/" + url.PathEscape(name) }

func driftBadge(d store.Drift) DriftBadge {
	var det versions.DriftDetail
	_ = json.Unmarshal(d.Detail, &det)
	b := DriftBadge{Kind: d.Kind, Since: d.Since}
	switch d.Kind {
	case "env":
		b.Label = "behind " + det.OtherIn
		b.Title = fmt.Sprintf("%s runs %s; this environment runs %s", det.OtherIn, det.Other, det.Running)
	case "upstream":
		b.Label = "upstream " + det.Other
		b.Title = fmt.Sprintf("%s is available (%s); running %s", det.Other, det.Jump, det.Running)
	case "eol":
		b.Label = "end of life"
		if det.EOL != "" {
			if t, err := time.Parse("2006-01-02", det.EOL); err == nil && t.After(time.Now()) {
				b.Label = "EOL " + det.EOL
			}
		}
		b.Title = fmt.Sprintf("Release cycle %s ends support on %s (endoflife.date)", det.Other, orDash(det.EOL))
	case "declared":
		b.Label = "Git says " + det.Other
		b.Title = fmt.Sprintf("The Compose files declare %s; running %s", det.Other, det.Running)
	case "inconsistent":
		b.Label = "targets disagree"
		var parts []string
		for t, v := range det.Targets {
			parts = append(parts, t+": "+v)
		}
		sort.Strings(parts)
		b.Title = strings.Join(parts, ", ")
	}
	return b
}

func buildGrid(o versions.Overview, agents int, groupBy string) MatrixGrid {
	g := MatrixGrid{Unmapped: o.Matrix.Unmapped, Targets: len(o.Targets), Agents: agents, Updated: time.Now(), GroupBy: matrixGroupBy(groupBy)}
	perEnv := map[string]int{}
	for _, t := range o.Targets {
		perEnv[t.EnvironmentID]++
	}
	for _, e := range o.Matrix.Environments {
		g.Envs = append(g.Envs, EnvHeader{Name: e.Name, Targets: perEnv[e.ID]})
	}
	for _, row := range o.Matrix.Rows {
		r := MatrixRow{
			Service: row.Service.Name, Owner: row.Service.Owner, URL: serviceURL(row.Service.Name),
			App: row.App, AppSource: row.AppSource,
		}
		var drifts []DriftBadge
		for ei, c := range row.Cells {
			var cell MatrixCell
			var stale []string
			cell.FromDeclared = c.FromDeclared
			for _, v := range c.Versions {
				cell.Versions = append(cell.Versions, VersionView{Tag: v.Tag, Running: v.Running, Targets: strings.Join(v.Targets, ", ")})
				for i, id := range v.TargetIDs {
					if last, ok := o.Stale[id]; ok {
						stale = append(stale, v.Targets[i]+" last reported "+last.UTC().Format("2006-01-02 15:04 UTC"))
					}
				}
			}
			if len(stale) > 0 {
				cell.StaleTitle = strings.Join(stale, "; ")
			}
			for _, d := range o.DriftsAt(row.Service.ID, o.Matrix.Environments[ei].ID) {
				cell.Drifts = append(cell.Drifts, driftBadge(d))
			}
			drifts = append(drifts, cell.Drifts...)
			r.Cells = append(r.Cells, cell)
		}
		ref := o.Refs[row.Service.ID]
		if u, ok := o.Upstreams[row.Service.ID]; ok && u.HasLatest {
			r.Latest = u.Latest.Raw
			r.LatestURL = o.ReleaseURL[row.Service.ID+"|"+u.Latest.Raw]
			if u.LatestAny.Raw != u.Latest.Raw {
				r.Note = u.LatestAny.Raw + " outside pin"
			}
		} else if msg := o.CheckErrs[row.Service.ID]; msg != "" {
			r.Note = "check failed"
			r.NoteTitle = msg
		} else if versions.CheckedByAgent(row.Service, ref.Repo) {
			r.Note = "checked by agent"
		} else if ref.Repo != "" {
			r.Note = "not checked yet"
		}
		r.Health = healthOf(drifts, true)
		g.Rows = append(g.Rows, r)
	}
	g.Groups = groupRows(g.Rows, g.GroupBy)
	return g
}

// EventView is one history line.
type EventView struct {
	At         time.Time
	Type       string
	Label      string
	Text       string
	Service    string
	ServiceURL string
	Env        string
	Target     string
}

var eventLabels = map[string]string{
	"deployed": "deployed", "version_changed": "changed", "removed": "removed", "new_release": "release",
	"drift_detected": "drift", "drift_resolved": "resolved",
}

// ruleEventLabels name event types as the rule form does.
var ruleEventLabels = map[string]string{
	"new_release": "new releases", "drift_detected": "drift", "drift_resolved": "drift resolved",
	"version_changed": "deploys", "deployed": "first deploys", "removed": "removals", "agent_stale": "stale agents",
}

func eventViews(evs []store.Event, o versions.Overview) []EventView {
	targetName := map[string]string{}
	for _, t := range o.Targets {
		targetName[t.ID] = t.Name
	}
	out := make([]EventView, 0, len(evs))
	for _, e := range evs {
		svc := o.Services[e.ServiceID]
		it := notifier.Item{
			Type: e.Type, Service: svc.Name, Environment: o.Envs[e.EnvironmentID].Name,
			Target: targetName[e.TargetID], From: e.FromVersion, To: e.ToVersion, Note: e.Note,
		}
		v := EventView{
			At: e.At, Type: e.Type, Label: eventLabels[e.Type], Text: notifier.Describe(it), Service: svc.Name,
			Env: it.Environment, Target: it.Target,
		}
		if svc.Name != "" {
			v.ServiceURL = serviceURL(svc.Name)
		}
		out = append(out, v)
	}
	return out
}

// ServiceView is a service page.
type ServiceView struct {
	Base
	Name           string
	Owner          string
	Kind           string
	Upstream       string
	RefRepo        string
	Latest         string
	LatestAny      string
	CheckedAt      time.Time
	CheckError     string
	Private        bool // the agents check the upstream
	PublicRegistry bool // the server can try it: "check now"
	Policy         PolicyForm
	Envs           []ServiceEnv
	Releases       []ReleaseView
	PolicyFrom     string // service, catalog or default
	// Where release dates and notes come from: a GitHub owner/repo or a changelog URL,
	// and why (policy, catalog or image label).
	NotesGitHub, NotesGitLab, NotesChangelog, NotesFrom string
	Events                                              []EventView
	Versions                                            Timeline
	Acks                                                []AckView
	Runs                                                bool // some environment runs the service
}

// ReleaseView is one upstream release on a service page.
type ReleaseView struct {
	Version   string
	Published time.Time
	URL       string
}

// PolicyForm holds the version policy as form values.
type PolicyForm struct {
	TagFilter  string
	Track      string
	PinMajor   string
	Prerelease bool
}

// ServiceEnv is one environment on a service page.
type ServiceEnv struct {
	Name     string
	Versions []VersionView
	Drifts   []DriftBadge
}

// AckView is an acknowledgement shown on a service page.
type AckView struct {
	ID      string
	Kind    string
	Env     string
	Until   string
	By      string
	Created time.Time
	Active  bool
}

// PromotionView is a version waiting to be promoted.
type PromotionView struct {
	Service, From, To, Version, Running string
	Since                               time.Time
	Releases                            []ReleaseView
}

// PromotionsView is the delivery page: pending promotions and delivery statistics.
type PromotionsView struct {
	Base
	Deploys    BarChart
	Drift      LineChart
	Promotions []PromotionView
	EnvNames   []string // delivery table columns
	LeadNames  []string // "dev → staging", …
	Delivery   []DeliveryRow
}

// DeliveryRow is one service's delivery statistics over the last 30 days.
type DeliveryRow struct {
	Service string
	Envs    []DeliveryCell
	Leads   []string // median lead time per environment pair, "—" when unknown
}

// DeliveryCell is one environment of a service.
type DeliveryCell struct {
	Deploys int
	Last    time.Time
}

// EventsView is the history page.
type EventsView struct {
	Base
	Events     []EventView
	Service    string
	Env        string
	Type       string
	Since      string // 1h, 6h, 24h or 7d; empty means any time
	Services   []string
	EnvNames   []string
	Types      []string
	NextBefore string
	MoreURL    string
}

// InboxItem is one unmapped workload.
type InboxItem struct {
	TargetID   string
	WorkloadID string
	Target     string
	Env        string
	Namespace  string
	Workload   string
	Kind       string
	Image      string
	Repo       string
	Suggested  string
}

// InboxGroup is the unmapped workloads that run one image repository.
type InboxGroup struct {
	Repo      string
	Suggested string
	Tags      []string
	Envs      []string
	Workloads []InboxItem
}

// InboxView is the inbox page.
type InboxView struct {
	Base
	Groups   []InboxGroup
	Services []string
	Rules    []MappingRuleView
}

// MappingRuleView is one mapping rule on the inbox page.
type MappingRuleView struct {
	ID      string
	Match   string
	Pattern string
	Service string // empty for ignore rules
	Created time.Time
}

// AgentView is one agent row.
type AgentView struct {
	ID        string
	Name      string
	Status    string // online, stale, never, revoked
	Outdated  bool   // older than the server
	Version   string
	Hostname  string
	Platforms string
	LastSeen  time.Time
}

// TargetView is one target row.
type TargetView struct {
	ID           string
	AgentID      string // empty when the server collects it
	Name         string
	Platform     string
	Env          string
	Agent        string
	Status       string
	Error        string
	LastSnapshot time.Time
}

// AgentsView is the agents and targets page.
type AgentsView struct {
	Base
	Agents     []AgentView
	Targets    []TargetView
	EnvNames   []string
	AgentNames []string
	ServerURL  string
	Moves      []AgentOption
	Envs       []EnvView
}

// EnvView is one environment on the agents page.
type EnvView struct {
	ID       string
	Name     string
	Position int
	Targets  int
}

// TargetEditView is the page that edits one target.
type TargetEditView struct {
	Base
	ID       string
	Name     string
	Platform string
	EnvID    string
	Envs     []EnvView
	Settings string
	Poll     int
	Agent    string
	AgentID  string
}

// AgentOption is an agent a target can move to.
type AgentOption struct {
	ID   string
	Name string
}

// AgentPageView is one agent with its token and targets.
type AgentPageView struct {
	Base
	Agent         AgentView
	Registered    time.Time
	Created       time.Time
	Tokens        []store.AgentToken
	Targets       []TargetView
	Moves         []AgentOption
	NewToken      string
	Rotated       bool
	ServerURL     string
	ServerVersion string
}

// ChannelView is one notification channel.
type ChannelView struct {
	ID     string
	Name   string
	Type   string
	Detail string
}

// RuleView is one notification rule.
type RuleView struct {
	ID      string
	Paused  bool
	Channel string
	Events  string
	Mode    string
	Filter  string
}

// NotificationsView is the notifications page.
type NotificationsView struct {
	Base
	Channels []ChannelView
	Rules    []RuleView
	SMTP     bool
}

// UserView is one user row.
type UserView struct {
	ID          string
	Email       string
	Role        string // role in the current workspace; empty for organization-wide users or no access
	Access      string
	OrgWide     bool
	OrgRole     string // owner or admin for organization-wide users
	LastLogin   time.Time
	IsSelf      bool
	HasPassword bool
	TOTP        bool
}

// TokenView is one API token on the Users page.
type TokenView struct {
	ID        string
	Name      string
	Role      string
	CreatedBy string
	CreatedAt time.Time
	LastUsed  time.Time
	ExpiresAt time.Time
	Expired   bool
}

// AuditView is one audit log line.
type AuditView struct {
	At      time.Time
	Actor   string
	Action  string
	Details string
}

// SettingsView is the users and tokens page.
type SettingsView struct {
	Base
	CanGrantOwner bool
	Require2FA    bool
	SelfHas2FA    bool
	AppLabel      string // the workspace's own application label key
	Audit         []AuditView
	Users         []UserView
	Tokens        []TokenView
	Secret        string
	SecretLabel   string
}

// LoginView is the sign-in page.
type LoginView struct {
	Sent       bool
	Error      string
	OIDC       string // provider name; empty when OIDC is off
	Mail       bool
	Password   bool
	NoAccounts bool // nobody has an account yet: point to the setup link
}

// relTime formats a time for <time datetime>.
func iso(t time.Time) string { return t.UTC().Format(time.RFC3339) }

// short formats a time as a fallback text before JavaScript runs.
func short(t time.Time) string {
	if t.IsZero() {
		return "never"
	}
	return t.UTC().Format("2006-01-02 15:04 UTC")
}

func itoa(n int) string { return fmt.Sprint(n) }

// rowSearch is what the matrix filter matches a row against: service, owner, versions and targets.
func rowSearch(r MatrixRow) string {
	parts := []string{r.Service, r.Owner, r.Latest, r.App}
	for _, c := range r.Cells {
		for _, v := range c.Versions {
			parts = append(parts, v.Tag, v.Targets)
		}
		for _, d := range c.Drifts {
			parts = append(parts, d.Label)
		}
	}
	return strings.ToLower(strings.Join(parts, " "))
}

// rowDrift is "1" when any environment of the row has drift, for "Only with drift".
func rowDrift(r MatrixRow) string {
	for _, c := range r.Cells {
		if len(c.Drifts) > 0 {
			return "1"
		}
	}
	return ""
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

// htmxConfig makes htmx swap error responses too: they carry an explanation.
const htmxConfig = `{"responseHandling":[{"code":"204","swap":false},{"code":"[23]..","swap":true},{"code":"[45]..","swap":true,"error":false}]}`

// ErrorView is an error page.
type ErrorView struct {
	Status   int
	Title    string
	Message  string
	SignedIn bool
}

// envLabel names the environment of a matrix column, for the phone layout.
func envLabel(envs []EnvHeader, i int) string {
	if i < len(envs) {
		return envs[i].Name
	}
	return ""
}

// isSettings reports whether a page belongs under the Settings menu.
func isSettings(page string) bool {
	switch page {
	case "agents", "notifications", "settings", "workspaces":
		return true
	}
	return false
}

// initial is the first letter of an e-mail address, for the account button.
func initial(email string) string {
	for _, r := range email {
		return strings.ToUpper(string(r))
	}
	return "?"
}

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

func kindLabel(k string) string {
	if k == "third_party" {
		return "third-party"
	}
	return "own"
}

func envSuffix(env string) string {
	if env == "" {
		return ""
	}
	return " in " + env
}

func nsSuffix(ns string) string {
	if ns == "" {
		return ""
	}
	return " · " + ns
}

func workspaceRoles() []string { return []string{"viewer", "member", "admin", "none"} }

func roleLabel(r string) string {
	if r == "none" {
		return "no access"
	}
	return r
}

// exportURL is the inventory CSV, now or as of a past time.
// matrixURL is the matrix grouped by group, as of at (empty: now).
func matrixURL(group, at string) string {
	q := url.Values{"group": {group}}
	if at != "" {
		q.Set("at", at)
	}
	return "/?" + q.Encode()
}

func exportURL(at string) string {
	if at == "" {
		return "/api/v1/inventory?format=csv"
	}
	return "/api/v1/inventory?format=csv&at=" + url.QueryEscape(at)
}

// ReportView is the monthly report.
type ReportView struct {
	Base
	Month, Generated                 string
	Prev, PrevLabel, Next, NextLabel string
	Services, Targets, Deploys       int
	Attention                        []AttentionView
	EnvNames, LeadNames              []string
	Delivery                         []DeliveryRow
	Releases                         []ReportReleaseView
}

// AttentionView is one open drift in the report.
type AttentionView struct {
	Service, Environment, Kind, Label, Text, Since string
}

// ReportReleaseView is one new upstream release in the report.
type ReportReleaseView struct {
	Service, Version, At string
}

// attention describes an open drift in words.
func attention(kind string, d versions.DriftDetail) (label, text string) {
	switch kind {
	case "eol":
		verb := "ends"
		if t, err := time.Parse("2006-01-02", d.EOL); err == nil && t.Before(time.Now()) {
			verb = "ended"
		}
		return "end of life", fmt.Sprintf("runs %s; release cycle %s %s support on %s", d.Running, d.Other, verb, orDash(d.EOL))
	case "declared":
		return "differs from Git", fmt.Sprintf("runs %s; Git declares %s", d.Running, d.Other)
	case "upstream":
		return "behind upstream", fmt.Sprintf("runs %s; %s is available (%s)", d.Running, d.Other, d.Jump)
	case "env":
		return "behind " + d.OtherIn, fmt.Sprintf("runs %s; %s runs %s", d.Running, d.OtherIn, d.Other)
	case "inconsistent":
		return "targets disagree", "targets of this environment run different versions"
	}
	return kind, d.Running
}

// HygieneView is the image hygiene page.
type HygieneView struct {
	Base
	Findings []versions.Finding
	Kinds    []HygieneKind
	Kind     string // the kind shown; empty for all
	Total    int
}

// HygieneKind is one kind of finding with its count, for the summary.
type HygieneKind struct {
	Kind  string
	Label string
	Help  string
	Count int
	Warn  bool
}
