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
	Unmapped int
	Targets  int
	Agents   int
	Updated  time.Time
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

func buildGrid(o versions.Overview, agents int) MatrixGrid {
	g := MatrixGrid{Unmapped: o.Matrix.Unmapped, Targets: len(o.Targets), Agents: agents, Updated: time.Now()}
	perEnv := map[string]int{}
	for _, t := range o.Targets {
		perEnv[t.EnvironmentID]++
	}
	for _, e := range o.Matrix.Environments {
		g.Envs = append(g.Envs, EnvHeader{Name: e.Name, Targets: perEnv[e.ID]})
	}
	for _, row := range o.Matrix.Rows {
		r := MatrixRow{Service: row.Service.Name, Owner: row.Service.Owner, URL: serviceURL(row.Service.Name)}
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
		} else if ref.Repo != "" && !versions.IsPublicRegistry(ref.Repo) {
			r.Note = "checked by agent"
		} else if ref.Repo != "" {
			r.Note = "not checked yet"
		}
		g.Rows = append(g.Rows, r)
	}
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
	Name       string
	Owner      string
	Kind       string
	Upstream   string
	RefRepo    string
	Latest     string
	LatestAny  string
	CheckedAt  time.Time
	CheckError string
	Private    bool
	Policy     PolicyForm
	Envs       []ServiceEnv
	Releases   []ReleaseView
	PolicyFrom string // service, catalog or default
	// Where release dates and notes come from: a GitHub owner/repo or a changelog URL,
	// and why (policy, catalog or image label).
	NotesGitHub, NotesGitLab, NotesChangelog, NotesFrom string
	Events                                              []EventView
	Acks                                                []AckView
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
	Audit         []AuditView
	Users         []UserView
	Tokens        []TokenView
	Secret        string
	SecretLabel   string
}

// LoginView is the sign-in page.
type LoginView struct {
	Sent     bool
	Error    string
	OIDC     string // provider name; empty when OIDC is off
	Mail     bool
	Password bool
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
	parts := []string{r.Service, r.Owner, r.Latest}
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
}
