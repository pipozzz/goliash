// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package ui

import (
	"encoding/json"
	"fmt"
	"net/url"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/pipozzz/goliash/internal/mapping"
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
	Unread     int // notifications not seen yet
	Notice     string
	Error      string
	CanMember  bool
	CanAdmin   bool
	OrgAdmin   bool // organization owner or admin: manages workspaces
	Kiosk      bool // a full-screen page without the header (the tiles' TV mode)
	Workspace  string
	Workspaces []WorkspaceOption
	Demo       bool   // a public, read-only demo
	PreviewPRs bool   // everyone may preview pull requests: the demo's, which open none
	Goli       bool   // Goli, the assistant, answers questions
	GoliVia    string // who answers for Goli: the model's provider, which sees questions and what Goli reads
	Freeze     string // the change freeze the workspace is in: "until 3 Jan 08:00 UTC: Christmas"; "" outside one
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
	Views   ViewTabs
	Grid    MatrixGrid
	Hygiene int    // image hygiene warnings
	At      string // "2006-01-02T15:04" (UTC) when looking back; empty for now
	// Yesterday is where "Time travel" opens without JavaScript.
	Yesterday string
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
	Steps    []Step           // first-run checklist, while the matrix is empty
	Explore  bool             // the viewer may open an Example workspace with example data
	Unread   []UnreadRegistry // private registries whose services have no known release
	Past     bool             // the matrix as of a moment in the past
	CanAdmin bool
	gitops   []versions.GitOpsApp // the GitOps applications the grid was built with, for markPRs
}

// MatrixGroup is a run of matrix rows under one heading.
type MatrixGroup struct {
	Name, Caption string
	URL           string // grouped by target: the target's page
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
	{"app", "Application"}, {"team", "Team"}, {"target", "Target"}, {"status", "Status"}, {"none", "None"},
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
	targets := map[string]TargetTag{}
	if by == "target" {
		rows, targets = rowsByTarget(rows)
	}
	byKey := map[string][]MatrixRow{}
	sources := map[string]map[string]bool{}
	fams := rowFamilies(rows)
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
		case "target":
			key = r.Target
			if key == "" {
				key = "not running"
			}
		default:
			key = fams[r.App].Name
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
	last := func(k string) bool { return k == "other" || k == "no owner" || k == "not running" }
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
		envOf := map[string]int{} // by target: its environment's column, so dev's targets come before prod's
		if by == "target" {
			for k, rs := range byKey {
				envOf[k] = len(rs[0].Cells)
				for i, c := range rs[0].Cells {
					if len(c.Versions) > 0 {
						envOf[k] = i
						break
					}
				}
			}
		}
		sort.Slice(keys, func(i, j int) bool {
			if last(keys[i]) != last(keys[j]) {
				return last(keys[j])
			}
			if envOf[keys[i]] != envOf[keys[j]] {
				return envOf[keys[i]] < envOf[keys[j]]
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
		if by == "app" {
			g.Caption = familyCaption(fams, k, sources[k])
		}
		if t, ok := targets[k]; ok {
			g.Caption = t.Caption
			if t.ID != "" {
				g.URL = "/targets/" + url.PathEscape(t.ID)
			}
		}
		out = append(out, g)
	}
	return out
}

// rowsByTarget splits each row into one per target it runs on, each keeping only that target's versions (and its
// environment's drifts): the services of a cluster or a cloud account. A row running nowhere is kept once, without
// a target. It returns the targets by name too.
func rowsByTarget(rows []MatrixRow) ([]MatrixRow, map[string]TargetTag) {
	var out []MatrixRow
	byName := map[string]TargetTag{}
	for _, r := range rows {
		var targets []string
		for _, c := range r.Cells {
			for _, v := range c.Versions {
				for _, t := range v.On {
					if !slices.Contains(targets, t.Name) {
						targets = append(targets, t.Name)
						byName[t.Name] = t
					}
				}
			}
		}
		if len(targets) == 0 {
			out = append(out, r)
			continue
		}
		for _, t := range targets {
			cp := r
			cp.Target = t
			cp.Cells = make([]MatrixCell, len(r.Cells))
			for i, c := range r.Cells {
				var vs []VersionView
				for _, v := range c.Versions {
					if slices.ContainsFunc(v.On, func(x TargetTag) bool { return x.Name == t }) {
						vs = append(vs, v)
					}
				}
				if len(vs) > 0 {
					c.Versions = vs
					cp.Cells[i] = c
				}
			}
			var all []DriftBadge // its health, from what is left: another environment's drift is not this target's
			for _, c := range cp.Cells {
				all = append(all, c.Drifts...)
			}
			cp.Health = healthOf(all, true)
			if slices.ContainsFunc(all, func(b DriftBadge) bool { return b.Kind == "exploited" }) {
				cp.Health = "bad"
			}
			out = append(out, cp)
		}
	}
	return out, byName
}

func rowFamilies(rows []MatrixRow) map[string]versions.AppFamily {
	names := make([]versions.AppName, 0, len(rows))
	for _, r := range rows {
		names = append(names, versions.AppName{App: r.App, Source: r.AppSource})
	}
	return versions.AppFamilies(names)
}

// familyCaption says where a group's name came from: the applications merged into
// it, or the one source of its name.
func familyCaption(fams map[string]versions.AppFamily, name string, sources map[string]bool) string {
	for _, f := range fams {
		if f.Name == name && len(f.Members) > 1 {
			return strings.Join(f.Members, " · ")
		}
	}
	if len(sources) == 1 {
		for src := range sources {
			return appSourceLabel(src)
		}
	}
	return ""
}

// Step is one item of the first-run checklist.
type Step struct {
	Title    string
	Text     string
	Href     string
	Action   string
	Done     bool
	Optional bool
	Next     bool // the step to take now: its button is the primary one
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
	Split     bool   // the service is compared per application; this row is its part in App
	Health    string // ok, warn or bad, from the row's drift
	URL       string
	Cells     []MatrixCell
	Latest    string
	LatestURL string
	Note      string // e.g. "17.0 outside pin", "checked by agent"
	NoteTitle string
	Target    string                  // grouped by target: the one target this row shows
	Unknown   versions.UnknownService // why the newest release is not known; empty when it is
}

// CellPR is a pull request a matrix cell offers or links: to preview, or opened already.
type CellPR struct {
	URL, Label, Title string
	Open              bool // opened already: a link to it, not to its preview
}

// MatrixCell is one service in one environment.
type MatrixCell struct {
	PRs          []CellPR
	Versions     []VersionView
	FromDeclared bool // nothing reports what runs; the versions come from Compose files
	Drifts       []DriftBadge
	StaleTitle   string // set when some of the data comes from targets that stopped reporting
}

// VersionView is one tag running in a cell.
type VersionView struct {
	Tag      string
	Resolved string // the exact release behind a moving tag
	Running  int
	Targets  string
	On       []TargetTag  // the targets it runs on, for grouping by target
	Drifts   []DriftBadge // the drifts about this version, on a service page
}

// TargetTag is a target a version runs on, named and described for a heading: "Kubernetes · prod".
type TargetTag struct{ ID, Name, Caption string }

// DriftBadge is a short drift label with an explanation on hover.
type DriftBadge struct {
	Kind    string
	Label   string
	Title   string
	Since   time.Time
	Running string // the version the drift is about; empty for drifts of the whole environment
}

func serviceURL(name string) string { return "/services/" + url.PathEscape(name) }

func driftBadge(d store.Drift) DriftBadge {
	var det versions.DriftDetail
	_ = json.Unmarshal(d.Detail, &det)
	b := DriftBadge{Kind: d.Kind, Since: d.Since, Running: det.Running}
	switch d.Kind {
	case "env":
		b.Label = "behind " + det.OtherIn
		b.Title = fmt.Sprintf("%s runs %s; this environment runs %s", det.OtherIn, det.Other, det.Running)
	case "upstream":
		b.Label = "upstream " + det.Other
		b.Title = fmt.Sprintf("%s is available (%s); running %s%s", det.Other, det.Jump, det.Running, onTargets(det.On))
	case "eol":
		b.Label = "end of life"
		if det.EOL != "" {
			if t, err := time.Parse("2006-01-02", det.EOL); err == nil && t.After(time.Now()) {
				b.Label = "EOL " + det.EOL
			}
		}
		b.Title = fmt.Sprintf("Release cycle %s ends support on %s (endoflife.date); running %s%s", det.Other, orDash(det.EOL), det.Running, onTargets(det.On))
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
		for _, part := range row.Units() {
			g.Rows = append(g.Rows, matrixRow(o, row, part))
		}
	}
	g.Groups = groupRows(g.Rows, g.GroupBy)
	return g
}

// targetCaption describes a target for a heading: its platform and environment.
func targetCaption(o versions.Overview, id, env string) string {
	for _, t := range o.Targets {
		if t.ID == id {
			if p, ok := platformInfo(t.Platform); ok {
				return p.Title + " · " + env
			}
			return t.Platform + " · " + env
		}
	}
	return env
}

// matrixRow is one matrix line: a service, or its part in one application when its
// versions are compared per application.
func matrixRow(o versions.Overview, row versions.Row, part versions.Part) MatrixRow {
	r := MatrixRow{
		Service: row.Service.Name, Owner: row.Service.Owner, URL: serviceURL(row.Service.Name),
		App: row.App, AppSource: row.AppSource, Split: len(row.Parts) > 0,
	}
	var drifts []DriftBadge
	if r.Split {
		r.App, r.AppSource = part.App, part.AppSource
	}
	for ei, c := range part.Cells {
		var cell MatrixCell
		var stale []string
		cell.FromDeclared = c.FromDeclared
		for _, v := range c.Versions {
			vv := VersionView{Tag: v.Tag, Resolved: v.Resolved, Running: v.Running, Targets: strings.Join(v.Targets, ", ")}
			for i, name := range v.Targets {
				caption := o.Matrix.Environments[ei].Name
				if i < len(v.TargetIDs) {
					caption = targetCaption(o, v.TargetIDs[i], caption)
				}
				tag := TargetTag{Name: name, Caption: caption}
				if i < len(v.TargetIDs) {
					tag.ID = v.TargetIDs[i]
				}
				vv.On = append(vv.On, tag)
			}
			cell.Versions = append(cell.Versions, vv)
			for i, id := range v.TargetIDs {
				if last, ok := o.Stale[id]; ok {
					stale = append(stale, v.Targets[i]+" last reported "+last.UTC().Format("2006-01-02 15:04 UTC"))
				}
			}
		}
		if len(stale) > 0 {
			cell.StaleTitle = strings.Join(stale, "; ")
		}
		for _, d := range o.DriftsIn(row.Service.ID, part.App, o.Matrix.Environments[ei].ID) {
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
	} else if u, ok := versions.WhyUnknown(o, row.Service.ID); ok {
		r.Note, r.NoteTitle = u.Short, strings.TrimSpace(u.Reason+" "+u.Fix)
		r.Unknown = versions.UnknownService{Service: row.Service.Name, Repo: ref.Repo, Unknown: u}
	}
	r.Health = healthOf(drifts, true)
	return r
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
	Ask        string // what to ask Goli about what happened around this event; empty without Goli
}

// askAround fills in, for changes that can start an incident, what to ask Goli about everything else that changed
// around the same time.
func askAround(events []EventView) {
	for i, e := range events {
		switch e.Type {
		case "deployed", "version_changed", versions.RollbackNote, "removed", "drift_detected":
			events[i].Ask = "What else changed in the hour before and after " + e.At.UTC().Format("2006-01-02 15:04 UTC") +
				", when this happened: " + e.Text + "? Could any of it be related, and what should I check first?"
		}
	}
}

var eventLabels = map[string]string{
	"deployed": "deployed", "version_changed": "changed", "removed": "removed", "new_release": "release",
	"drift_detected": "drift", "drift_resolved": "resolved",
}

// ruleEventLabels name event types as the rule form does.
var ruleEventLabels = map[string]string{
	"new_release": "new releases", "drift_detected": "drift", "drift_resolved": "drift resolved",
	"version_changed": "deploys", "rollback": "rollbacks", "deployed": "first deploys", "removed": "removals", "agent_stale": "stale agents", "rollout": "stuck rollouts", "goliash_release": "new Goliash releases", "gitops": "Argo CD", "pull_request": "pull requests",
	"vulnerability": "vulnerabilities", "security_release": "security releases", "deadline": "remediation deadlines", "watch": "security watches", "updates_plan": "upgrade plan", "posture_report": "security posture report", "security_brief": "security brief", "image_grew": "image growth", "image_over_budget": "image over budget", "outdated_base": "outdated base", "evidence_lost": "signature or SBOM lost", "unexpected_signer": "unexpected signer", "license": "AGPL or source-available packages",
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
			Type: e.Type, Service: svc.Name, App: e.App, Environment: o.Envs[e.EnvironmentID].Name,
			Target: targetName[e.TargetID], From: e.FromVersion, To: e.ToVersion, Note: versions.EventNote(e),
		}
		v := EventView{
			At: e.At, Type: e.Type, Label: eventLabels[e.Type], Text: notifier.Describe(it), Service: svc.Name,
			Env: it.Environment, Target: it.Target,
		}
		if it.Note == versions.RollbackNote {
			v.Type, v.Label = versions.RollbackNote, "rolled back"
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
	Rollouts       []versions.RolloutIssue // its workloads whose replicas are not where they should be
	GitOps         []GitOpsView            // the Argo CD applications that deploy it
	TeamNames      []string                // the workspace's teams, to pick one
	Security       ServiceSecurity
	Sizes          LineChart   // the image's size per environment over the last 90 days, in MB; empty when unknown
	SizeChange     *SizeChange // what the last deploy changed in the image, when its layers are known
	Name           string
	Owner          string
	OwnerSource    string // "" set by people, "label" or "app" when Goliash set it
	App            string // placed in this application by hand; empty: from its labels
	Kind           string
	Upstream       string
	RefRepo        string
	KindShown      string // own or third_party, as chosen or worked out from the image
	KindAuto       string // the kind worked out from the image, offered as the default
	KindChosen     bool
	Latest         string
	LatestAny      string
	CheckedAt      time.Time
	CheckError     string
	Unknown        versions.UnknownService // why the newest release is not known; empty when it is
	Private        bool                    // the agents check the upstream
	PublicRegistry bool                    // the server can try it: "check now"
	Policy         PolicyForm
	BuiltOn        *versions.BaseStatus // the image the service is built on, when known
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
	Badges                                              []BadgeView
	// Split lists the applications the service's workloads belong to, when more than
	// one, with the name each would get as a service of its own.
	Split []SplitApp
	// Manual lists where people entered that the service runs outside Goliash, and EnvNames the
	// environments to choose from.
	Manual   []ManualEntry
	EnvNames []string
	// Platforms are those of the targets it runs on, and CanPR whether the server opens pull requests: without a
	// GitOps panel, the page says how to name the service's place in Git.
	Platforms []string
	CanPR     bool
}

// SplitApp is one application of a service that runs in several.
type SplitApp struct {
	App       string
	Name      string // the service it would become; the first keeps the current name
	Workloads int
}

// BadgeView is one badge of a service, with the markdown that embeds it.
type BadgeView struct {
	Env      string // empty: every environment
	URL      string
	Markdown string
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
	BaseImage  string // set by people; empty: as the image declares
	MaxSizeMB  string // the image size budget, empty: none
	Signer     string // who should sign its images, empty: nobody named
	AutoPR     string // "dev=minor, staging=patch": pull requests the policy opens by itself
	Reviewers  string // "alice, @acme/payments": asked to review its pull requests
	Labels     string // "dependencies, goliash": put on its pull requests
}

// ServiceEnv is one environment on a service page.
type ServiceEnv struct {
	Name     string
	Versions []VersionView
	Drifts   []DriftBadge // drifts of the whole environment, or about a version no longer running
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
	Service, App, From, To, Version, Running string
	Since                                    time.Time
	Releases                                 []ReleaseView
	Vulns                                    versions.VulnDelta
	Licenses                                 versions.LicenseDelta
	PRs                                      []PromotePR // Argo CD applications it can be opened in as a pull request
}

// AppPromotionView is a whole application's promotion, offered as one pull request.
type AppPromotionView struct {
	App, Target, From, To string
	Services              []string // "checkout 2.3.1 → 2.4.0"
	URL                   string   // its preview
}

// PromotePR is one Argo CD application a promotion can be opened in as a pull request.
type PromotePR struct {
	TargetID, WorkloadID, Target, App string
	URL                               string // the pull request Goliash (or, with Title, someone else) opened already
	State                             string // open or merged
	Checks                            string // its checks: passing, failing, pending or ""
	Title                             string // someone else's pull request: its title
}

// PromotionsView is the delivery page: pending promotions and delivery statistics.
type PromotionsView struct {
	Base
	Deploys       BarChart
	Drift         LineChart
	Promotions    []PromotionView
	AppPromotions []AppPromotionView // whole applications waiting, each as one pull request
	Pulls         []PullView         // pull requests Goliash opened, newest first
	Rollouts      []versions.RolloutIssue
	EnvNames      []string // delivery table columns
	LeadNames     []string // "dev → staging", …
	Delivery      []DeliveryRow
}

// DeliveryRow is one service's delivery statistics over the last 30 days.
type DeliveryRow struct {
	Service string
	Envs    []DeliveryCell
	Leads   []string // median lead time per environment pair, "—" when unknown
}

// DeliveryCell is one environment of a service.
type DeliveryCell struct {
	Deploys   int
	Rollbacks int // changes back to an older version
	Last      time.Time
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
	App        string // its application, when a Compose project, Nomad job or label names it
	// OwnName is the service name "Map only this workload" suggests: its name, or with
	// its application when other applications have a workload of the same name.
	OwnName string
	InApp   bool // map it within its application (several "db" in several projects)
}

// InboxGroup is the unmapped workloads that run one image repository.
type InboxGroup struct {
	Repo      string
	Suggested string
	Tags      []string
	Envs      []string
	Workloads []InboxItem
	Apps      []string // the applications its workloads belong to, when known
}

// InboxView is the inbox page.
type InboxView struct {
	Base
	AutoMap  bool // new images map to their suggested service by themselves
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
	Enrolled  bool     // registers itself with an enrollment code
	Notes     []string // what it could not use when it last registered
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
	Codes      []CodeView
}

// CodeView is an enrollment code on the agents page.
type CodeView struct {
	ID       string
	Env      string
	Many     bool
	Agents   int
	Expires  time.Time // zero: never
	Expired  bool
	LastUsed time.Time
	By       string
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
	Managed  bool // the agent found or declared it and owns its settings
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
	Code          CodeInfo // for an enrolled agent: the code it registers with
	UpgradeTitle  string   // how to update an outdated agent
	UpgradeCmd    string
	Managed       int // targets the agent found itself, deleted with it
}

// CodeInfo describes the enrollment code an agent registers with.
type CodeInfo struct {
	Env     string
	Many    bool
	Revoked bool
}

// ChannelView is one notification channel.
type ChannelView struct {
	ID       string
	Name     string
	Type     string
	Detail   string
	Browsers int // web push: subscribed browsers, and the person's own
	Mine     int
}

// RuleView is one notification rule.
type RuleView struct {
	ID      string
	Paused  bool
	Plan    bool // it sends the upgrade plan
	Report  bool // it sends the security posture report
	Brief   bool // it sends the security brief
	Channel string
	Events  string
	Mode    string
	Filter  string
}

// RuleForm is the add or edit form of a notification rule.
type RuleForm struct {
	Channel    string
	Mode       string
	MinJump    string
	Events     []string
	Services   string
	Owners     string
	Envs       string
	Production bool // only the last environment
	Apps       string
	DigestHour int
	Timezone   string // empty: UTC
	QuietFrom  int    // -1: no quiet hours
	QuietTo    int
}

// NewRuleForm is the add form's defaults.
func NewRuleForm() RuleForm {
	return RuleForm{Mode: "instant", Events: []string{"new_release", "drift_detected", "agent_stale"}, DigestHour: 8, QuietFrom: -1, QuietTo: -1}
}

// RuleEditView is the page that edits one rule.
type RuleEditView struct {
	Base
	ID       string
	Form     RuleForm
	Channels []ChannelView
}

// NotificationsView is the notifications page.
type NotificationsView struct {
	Base
	Channels []ChannelView
	Rules    []RuleView
	SMTP     bool
	PushKey  string // the VAPID public key, when there is a push channel
	// PushWarning: pushes go out without a sender Apple accepts (no https public URL).
	PushWarning bool
	Deliveries  []DeliveryView
}

// DeliveryView is one recent notification and how its delivery went.
type DeliveryView struct {
	ID      string
	At      time.Time // sent, or created when not sent yet
	Channel string
	Type    string
	Text    string
	Count   int    // items in a digest
	Status  string // sent, waiting, retrying, failed
	Label   string
	Error   string
	Retry   bool // can be retried now
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

// TokenView is one API token on Workspace settings.
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
	Freezes       []FreezeView
	Apps          []store.OAuthGrant // apps people let read this workspace
	CanGrantOwner bool
	Require2FA    bool
	SelfHas2FA    bool
	AppLabel      string // the workspace's own application label key
	Registries    []RegistryView
	Unread        []UnreadRegistry // private registries only the agents read
	Encrypts      bool             // the server can store registry credentials
	RegistryHost  string           // prefilled from "Add credentials"
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
	NoAccounts bool   // nobody has an account yet: point to the setup link
	SetupLink  string // offered right after an installation, instead of the log
	Passkeys   bool   // this server can offer passkeys
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
			parts = append(parts, v.Tag, v.Resolved, v.Targets)
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
	case "agents", "notifications", "settings", "workspaces", "apps", "teams", "status":
		return true
	}
	return false
}

// initial is the first letter of a name: an e-mail address for the account button,
// an application for its avatar.
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

func exportURL(at string) string { return exportFormatURL("csv", at) }

// exportFormatURL is the inventory in format (csv, cyclonedx), at a point in time when at is set.
func exportFormatURL(format, at string) string {
	if at == "" {
		return "/api/v1/inventory?format=" + format
	}
	return "/api/v1/inventory?format=" + format + "&at=" + url.QueryEscape(at)
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
	Pulls                            ReportPulls
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
	Findings    []versions.Finding
	Kinds       []HygieneKind
	Kind        string // the kind shown; empty for all
	Total       int
	DigestHints []DigestHint // targets that report no digest at all, and what to do about it
}

// DigestHint is a target whose containers all lack a digest, with the likely reason for its platform.
type DigestHint struct {
	Target   string
	Platform string
	Images   int
	Advice   string
}

// HygieneKind is one kind of finding with its count, for the summary.
type HygieneKind struct {
	Kind  string
	Label string
	Help  string
	Count int
	Warn  bool
}

func orOther(app string) string {
	if app == "" {
		return "other"
	}
	return app
}

func twoDigits(n int) string { return fmt.Sprintf("%02d", n) }

func hasString(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// eventTitle explains an event kind in the rule form.
func eventTitle(kind string) string {
	if kind == "updates_plan" {
		return "The Updates list, what to upgrade first, at the digest time: daily, or on Mondays"
	}
	if kind == "image_grew" {
		return "A deploy whose image is at least a quarter and 20 MB bigger than the one it replaced"
	}
	switch kind {
	case "image_over_budget":
		return "A deploy whose image is bigger than the size budget in its service's version policy"
	case "outdated_base":
		return "An image built on a build of its base that has had a newer one for over two weeks"
	case "evidence_lost":
		return "A deploy whose image has no signature, SBOM or provenance though the one it replaced had"
	}
	if kind == "posture_report" {
		return "The Security page in one message for production: exposure days, end of life, accepted risk, the longest exposures; at the digest time, daily or on Mondays"
	}
	if kind == "security_brief" {
		return "What to look at today: new security releases of what runs, watches, exploited vulnerabilities, security upgrades waiting, fixes not deployed; daily or on Mondays, nothing when there is nothing"
	}
	return ""
}

func stableName(name string) string { return mapping.StableName(name) }

// oldestDrift is when the longest-open of the drifts began.
func oldestDrift(drifts []DriftBadge) time.Time {
	var t time.Time
	for _, d := range drifts {
		if t.IsZero() || d.Since.Before(t) {
			t = d.Since
		}
	}
	return t
}

// onTargets is " on a, b" for the targets a drift is about, or "".
func onTargets(targets []string) string {
	if len(targets) == 0 {
		return ""
	}
	return " on " + strings.Join(targets, ", ")
}

// TimeTravel is the timeline above the matrix or the tiles: Base is the page now, without a moment.
type TimeTravel struct {
	Kind, Base, Path, Group, At string
	Hidden                      [][2]string // the base's query, for the exact-moment form
}

func newTimeTravel(kind, base, group, at string) TimeTravel {
	t := TimeTravel{Kind: kind, Base: base, Group: group, At: at, Path: base}
	if u, err := url.Parse(base); err == nil {
		t.Path = u.Path
		keys := make([]string, 0, len(u.Query()))
		for k := range u.Query() {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if k != "at" {
				t.Hidden = append(t.Hidden, [2]string{k, u.Query().Get(k)})
			}
		}
	}
	return t
}

// pastDrifts gives the matrix of a past moment its drift as it was then, worked out from what ran: a version
// older than the newest release out by then, or older than the environment before it ran. The drift Goliash
// stored describes now and is not shown for the past.
func pastDrifts(g *MatrixGrid) {
	for ri := range g.Rows {
		r := &g.Rows[ri]
		latest, hasLatest := versions.ParseVersion(r.Latest)
		var prev versions.Version
		prevEnv, hasPrev := "", false
		for i := range r.Cells {
			c := &r.Cells[i]
			if len(c.Versions) == 0 {
				continue
			}
			tag := c.Versions[0].Tag
			if c.Versions[0].Resolved != "" {
				tag = c.Versions[0].Resolved
			}
			v, ok := versions.ParseVersion(tag)
			if !ok {
				continue
			}
			if hasLatest && v.Compare(latest) < 0 {
				c.Drifts = append(c.Drifts, DriftBadge{
					Kind: "upstream", Label: "upstream " + r.Latest, Running: tag,
					Title: fmt.Sprintf("%s was out by then; running %s", r.Latest, tag),
				})
			}
			if hasPrev && v.Compare(prev) < 0 {
				c.Drifts = append(c.Drifts, DriftBadge{
					Kind: "env", Label: "behind " + prevEnv, Running: tag,
					Title: fmt.Sprintf("%s ran %s then; this environment ran %s", prevEnv, prev.Raw, tag),
				})
			}
			if i < len(g.Envs) {
				prev, prevEnv, hasPrev = v, g.Envs[i].Name, true
			}
		}
		var all []DriftBadge
		for _, c := range r.Cells {
			all = append(all, c.Drifts...)
		}
		r.Health = healthOf(all, true)
	}
	g.Groups = groupRows(g.Rows, g.GroupBy) // the groups count rows by health
}

// markExploited badges every cell whose running image carries a vulnerability CISA lists as exploited in the
// wild, from the images' SBOMs: the most urgent thing a matrix can show. It describes now, not the past.
func markExploited(g *MatrixGrid, r versions.VulnReport) {
	cves := map[string][]string{} // service + "|" + environment -> CVEs
	for _, f := range r.Findings {
		if f.Exploited == nil {
			continue
		}
		seen := map[string]bool{}
		for _, a := range f.Affected {
			k := a.Service + "|" + a.Env
			if !seen[k] {
				seen[k] = true
				cves[k] = append(cves[k], f.Key)
			}
		}
	}
	if len(cves) == 0 {
		return
	}
	for ri := range g.Rows {
		r := &g.Rows[ri]
		marked := false
		for i := range r.Cells {
			if i >= len(g.Envs) || len(r.Cells[i].Versions) == 0 {
				continue
			}
			list := cves[r.Service+"|"+g.Envs[i].Name]
			if len(list) == 0 {
				continue
			}
			sort.Strings(list)
			label := "exploited " + list[0]
			if len(list) > 1 {
				label += fmt.Sprintf(" +%d", len(list)-1)
			}
			r.Cells[i].Drifts = append([]DriftBadge{{
				Kind: "exploited", Label: label,
				Title: "Exploited in the wild (CISA KEV), in the running image: " + strings.Join(list, ", "),
			}}, r.Cells[i].Drifts...)
			marked = true
		}
		if marked {
			r.Health = "bad"
		}
	}
	g.Groups = groupRows(g.Rows, g.GroupBy)
}

// markRollouts badges the cells whose workloads' replicas are not where they should be: a rollout stuck or a
// service down needs attention now; fewer replicas than asked for is a warning.
func markRollouts(g *MatrixGrid, issues []versions.RolloutIssue) {
	byCell := map[string][]versions.RolloutIssue{} // service + "|" + environment
	for _, is := range issues {
		byCell[is.Service+"|"+is.Environment] = append(byCell[is.Service+"|"+is.Environment], is)
	}
	if len(byCell) == 0 {
		return
	}
	for ri := range g.Rows {
		r := &g.Rows[ri]
		for i := range r.Cells {
			if i >= len(g.Envs) || len(r.Cells[i].Versions) == 0 {
				continue
			}
			list := byCell[r.Service+"|"+g.Envs[i].Name]
			if len(list) == 0 {
				continue
			}
			worst := list[0] // issues come worst first
			var details []string
			for _, is := range list {
				details = append(details, is.Target+rolloutWorkload(is)+": "+rolloutDetail(is))
			}
			b := DriftBadge{Kind: "rollout", Label: rolloutLabel(worst.Kind), Title: upperFirst(rolloutLabel(worst.Kind)) + " — " + strings.Join(details, "; ")}
			if worst.Kind == versions.RolloutShort {
				b.Kind, b.Label = "degraded", fmt.Sprintf("%d of %d replicas", worst.Running, worst.Desired)
			}
			r.Cells[i].Drifts = append([]DriftBadge{b}, r.Cells[i].Drifts...)
			r.Health = worseHealth(r.Health, healthOf([]DriftBadge{b}, true))
		}
	}
	g.Groups = groupRows(g.Rows, g.GroupBy)
}

// markGitOps badges the cells whose Argo CD application has been out of sync, degraded or missing for a while:
// what Git declares is not what runs, or what runs is not healthy. pinned, when not nil, is the version Git pins
// for an application out of sync, or "".
func markGitOps(g *MatrixGrid, apps []versions.GitOpsApp, now time.Time, pinned func(versions.GitOpsApp) string) {
	byCell := map[string][]versions.GitOpsApp{} // service + "|" + environment
	for _, a := range apps {
		if a.Trouble(now) != "" {
			byCell[a.Service+"|"+a.Environment] = append(byCell[a.Service+"|"+a.Environment], a)
		}
	}
	if len(byCell) == 0 {
		return
	}
	for ri := range g.Rows {
		r := &g.Rows[ri]
		for i := range r.Cells {
			if i >= len(g.Envs) {
				continue
			}
			list := byCell[r.Service+"|"+g.Envs[i].Name]
			if len(list) == 0 {
				continue
			}
			var details []string
			for _, a := range list {
				line := gitOpsLine(a, now)
				if pinned != nil {
					if p := pinned(a); p != "" {
						line += ", Git pins " + p + " (" + a.Tag + " runs)"
					}
				}
				details = append(details, line)
			}
			short := map[string]string{"flux": "Flux"}[list[0].Tool]
			if short == "" {
				short = "Argo"
			}
			b := DriftBadge{Kind: "gitops", Label: short + ": " + list[0].Trouble(now), Title: strings.Join(details, "; ")}
			r.Cells[i].Drifts = append(r.Cells[i].Drifts, b)
			r.Health = worseHealth(r.Health, "warn")
		}
	}
	g.Groups = groupRows(g.Rows, g.GroupBy)
}

// gitOpsLine says what an application's state is and for how long: "checkout-prod on k8s-prod: OutOfSync,
// Degraded for 2 hours".
func gitOpsLine(a versions.GitOpsApp, now time.Time) string {
	state := a.Sync
	if a.Health != "" {
		state += ", " + a.Health
	}
	return a.App + " on " + a.Target + ": " + strings.TrimPrefix(state, ", ") + " for " + versions.HumanDuration(now.Sub(a.SyncSince))
}

// worseHealth is the worse of two row healths.
func worseHealth(a, b string) string {
	rank := map[string]int{"ok": 0, "warn": 1, "bad": 2}
	if rank[b] > rank[a] {
		return b
	}
	return a
}

// ServiceSecurity is the service's running images with their supply-chain evidence, and the vulnerabilities their
// SBOMs show, the most urgent first.
type ServiceSecurity struct {
	Images           []ImageSecurity
	Findings         []ServiceFinding
	Total, Exploited int
	WithSBOM, More   int
}

// ImageSecurity is one running image of the service in one environment.
type ImageSecurity struct {
	Env, Image, Digest                string
	Checked, Signed, SBOM, Provenance bool
	Size                              int64 // bytes pulled; 0 when unknown
	Delta                             int64 // against the first (production) image's size, when both are known
	DeltaFrom                         string
	OldBase                           string // the base it is built on an older build of, when it is
	Rebuild                           string // the image down its chain of bases to rebuild first, when not itself
	Chain                             string // its chain of bases: "acme/base:1 (current) → alpine:3.20 (outdated)"
	Signer, SignerFull                string // who signed it, short and as the certificate names it
}

// ServiceFinding is one vulnerability in the service's images.
type ServiceFinding struct {
	Key, Summary, Severity, Package, Fix, Envs string
	Exploited                                  bool
}

// exploitedNote counts the exploited vulnerabilities among some: " (1 exploited)", or nothing.
func exploitedNote(list []versions.VulnRef) string {
	if n := versions.Exploited(list); n > 0 {
		return " (" + itoa(n) + " exploited)"
	}
	return ""
}

// vulnIDs lists vulnerabilities by ID, the first twelve.
func vulnIDs(list []versions.VulnRef) string {
	ids := make([]string, 0, min(len(list), 12))
	for _, v := range list[:min(len(list), 12)] {
		ids = append(ids, v.ID)
	}
	if len(list) > 12 {
		ids = append(ids, "…")
	}
	return strings.Join(ids, ", ")
}

// rolloutLabel names a rollout problem as people read it.
func rolloutLabel(k versions.RolloutKind) string {
	switch k {
	case versions.RolloutDown:
		return "down"
	case versions.RolloutStuck:
		return "rollout stuck"
	}
	return "short of replicas"
}

func rolloutBadge(k versions.RolloutKind) string {
	if k == versions.RolloutShort {
		return "degraded"
	}
	return "failing"
}

// rolloutHeadline says what is wrong with a workload, where.
func rolloutHeadline(is versions.RolloutIssue) string {
	return upperFirst(rolloutLabel(is.Kind)) + " in " + is.Environment + " on " + is.Target + rolloutWorkload(is) + ":"
}

// rolloutWorkload names the workload when its name is not the service's: a service
// runs in several workloads, and the one in trouble may be a leftover job.
func rolloutWorkload(is versions.RolloutIssue) string {
	name := is.Workload
	if i := strings.LastIndex(name, "/"); i >= 0 {
		name = name[i+1:]
	}
	if name == "" || name == is.Service {
		return ""
	}
	return ", " + is.Workload
}

// rolloutDetail says which versions run on how many of the replicas asked for.
func rolloutDetail(is versions.RolloutIssue) string {
	if is.Running == 0 {
		return "none of " + plural(is.Desired, "replica", "replicas") + " running"
	}
	parts := make([]string, 0, len(is.Versions))
	for _, v := range is.Versions {
		parts = append(parts, v.Version+" on "+itoa(v.Running))
	}
	return strings.Join(parts, ", ") + " of " + plural(is.Desired, "replica", "replicas") + " asked for"
}

// upperFirst upper-cases a label's first letter.
func upperFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// GitOpsView is a GitOps application (Argo CD, Flux) as it deploys one of a service's workloads.
type GitOpsView struct {
	Service                       string
	Tool                          string // argocd, flux
	Environment, Target, Workload string
	App, AppURL                   string // AppURL: the application in the Argo CD UI, with GOLIASH_ARGOCD_URL
	Sync, Health                  string
	Since                         time.Time
	Trouble                       string // out of sync, degraded, missing, or ""
	Repo, RepoURL                 string
	Path, TargetRevision          string
	Revision, CommitURL           string // Revision shortened for a commit
	CompareURL                    string // what changed in Git from the commit synced before, when known
	RevisionAt                    time.Time
	TargetID, WorkloadID          string
	Tag                           string // the version running
	To                            string // the version a pull request would pin; empty, no button
	PRURL                         string // the pull request Goliash opened for To, when it did
	PRState                       string // open or merged
	PRChecks                      string // its checks: passing, failing, pending or ""
	PRTitle                       string // the pull request someone else opened for To: its title
	Rollback, RollbackWhy         string // the version a rollback pull request would pin, degraded; empty, no button
	RollbackPRURL                 string // the rollback pull request Goliash opened, when it did
	GitPins                       string // out of sync: the version Git pins, when not the one running
	SyncWindow                    string // Argo CD's sync windows keep it from syncing now: why and until when
	ManualSync                    bool   // Argo CD syncs it only by hand
}

func gitOpsView(a versions.GitOpsApp, argoURL string, now time.Time) GitOpsView {
	v := GitOpsView{
		Tool: a.Tool, Environment: a.Environment, Target: a.Target, Workload: a.Workload, App: a.App,
		AppURL: argoAppURL(a, argoURL), Sync: a.Sync, Health: a.Health, Since: a.SyncSince,
		Trouble: a.Trouble(now), Repo: a.Repo, RepoURL: versions.RepoWebURL(a.Repo), Path: a.Path,
		TargetRevision: a.TargetRevision, Revision: a.Revision, TargetID: a.TargetID, WorkloadID: a.WorkloadID, Tag: a.Tag,
	}
	v.SyncWindow, v.ManualSync = syncWindowText(a.WorkloadGitOps, now), a.ManualSync
	v.CommitURL = versions.CommitWebURL(v.RepoURL, a.Revision)
	v.CompareURL, v.RevisionAt = versions.CompareWebURL(v.RepoURL, a.PrevRevision, a.Revision), a.RevisionAt
	if v.CommitURL != "" && len(v.Revision) > 7 {
		v.Revision = v.Revision[:7]
	}
	if v.RepoURL != "" { // shown as host/owner/repo
		v.Repo = strings.TrimPrefix(v.RepoURL, "https://")
	}
	return v
}

// argoAppURL is the application's page in the Argo CD UI, for an Argo CD application.
func argoAppURL(a versions.GitOpsApp, argoURL string) string {
	if a.Tool != "argocd" {
		return ""
	}
	return versions.ArgoAppURL(argoURL, a.AppNamespace, a.App)
}

// syncBadge is the badge class of an application's sync or health state.
func syncBadge(state string) string {
	switch state {
	case "Synced", "Healthy":
		return "ok"
	case "OutOfSync", "Progressing", "Suspended":
		return "warn"
	case "Degraded", "Missing":
		return "bad"
	}
	return "neutral"
}

// gitOpsTitle names the GitOps panel after the tools in it.
func gitOpsTitle(apps []GitOpsView) string {
	tools := map[string]bool{}
	for _, a := range apps {
		tools[a.Tool] = true
	}
	if len(tools) == 1 {
		for t := range tools {
			return versions.GitOpsToolName(t)
		}
	}
	return "GitOps"
}

// PullView is a pull request Goliash opened, and what became of it.
type PullView struct {
	Service, ServiceURL string
	Environment, Target string
	From, To            string
	URL, State          string // open, merged or closed
	Checks              string // passing, failing, pending or "", while open
	OpenedBy            string
	OpenedAt            time.Time
	Deployed            bool          // the version it pins runs there now
	ToRunning           time.Duration // from opening the pull request to the version running there; 0 unknown
	MergeToRunning      time.Duration // from merging it to the version running there; 0 unknown
	MergedAt            time.Time     // merged and not deployed: since when
	Waiting             string        // merged and not deployed: why, when Goliash can tell
}
