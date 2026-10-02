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
}

// MatrixView is the home page.
type MatrixView struct {
	Base
	Grid MatrixGrid
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
	Note      string // e.g. "17.0 outside pin", "checked by agent"
	NoteTitle string
}

// MatrixCell is one service in one environment.
type MatrixCell struct {
	Versions   []VersionView
	Drifts     []DriftBadge
	StaleTitle string // set when some of the data comes from targets that stopped reporting
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
	Releases   []string
	Events     []EventView
	Acks       []AckView
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
	Kind    string
	Env     string
	Until   string
	By      string
	Created time.Time
	Active  bool
}

// EventsView is the history page.
type EventsView struct {
	Base
	Events     []EventView
	Service    string
	Env        string
	Type       string
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

// InboxView is the inbox page.
type InboxView struct {
	Base
	Items    []InboxItem
	Services []string
}

// AgentView is one agent row.
type AgentView struct {
	Name      string
	Status    string // online, stale, never
	Version   string
	Hostname  string
	Platforms string
	LastSeen  time.Time
}

// TargetView is one target row.
type TargetView struct {
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
	NewToken   string
	NewAgent   string
	ServerURL  string
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
	ID        string
	Email     string
	Role      string
	LastLogin time.Time
	IsSelf    bool
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
	Audit       []AuditView
	Users       []UserView
	Secret      string
	SecretLabel string
}

// LoginView is the sign-in page.
type LoginView struct {
	Sent  bool
	Error string
	OIDC  string // provider name; empty when OIDC is off
	Mail  bool
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

func roles() []string { return []string{"viewer", "member", "admin", "owner"} }
