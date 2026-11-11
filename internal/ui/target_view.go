// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package ui

import (
	"context"
	"errors"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/pipozzz/goliash/internal/auth"
	"github.com/pipozzz/goliash/internal/store"
	"github.com/pipozzz/goliash/internal/versions"
	"github.com/pipozzz/goliash/pkg/agentproto"
)

// The target page shows one cluster or host: every workload as a card, coloured by
// how current it is, grouped by application (or namespace, team or status), with the target's health and recent
// changes on top.

// TargetDetailView is one target's page.
type TargetDetailView struct {
	Base
	ID, Name, Platform, Env string
	Agent, AgentID          string // empty AgentID: the server collects it
	Status, Error           string
	LastSnapshot            time.Time
	Stale                   bool
	CanAdmin                bool
	Where                   []WhereItem // where it runs, each a filter of the matrix; its platform is the badge

	Workloads, Services, OK, Drifting, Unmapped int
	Groups                                      []WorkloadGroup
	GroupBy                                     string // app, namespace, team or status
	GroupModes                                  []struct{ Key, Label string }
	Deploys                                     []int // per day, oldest first
	DeployMax                                   int
	Changes                                     []EventView
	// Server is the Linux server a host target is: its OS, what waits for it, its packages' vulnerabilities.
	Server *ServerView
}

// ServerView is what a Linux server's page says of it at the top.
type ServerView struct {
	OS, OSURL  string // Ubuntu 22.04, and its service page
	OSEnd      string // the end of its release's support, when known
	OSPast     bool
	Reboot     string // what waits for a reboot, "" when nothing
	Updates    string // package updates waiting, as update-notifier counted them
	Packages   int    // packages its agent sent for their vulnerabilities; 0 when none yet
	Vulns      int    // known vulnerabilities in them
	Exploited  int    // of them, exploited in the wild
	VulnsURL   string
	NotChecked string // why its packages are not checked, when they are not
	// PackageList is every package its agent sent, those with vulnerabilities first.
	PackageList []ServerPackage
}

// ServerPackage is one package of a Linux server and its known vulnerabilities.
type ServerPackage struct {
	Name, Version string
	Vulns         int
	Exploited     int
	Severity      string // the worst: CRITICAL, HIGH …
	Fix           string // the newest version that fixes one of them: upgrading to it fixes all those with a fix
	FixNewer      bool   // some are fixed in a newer version, which one not read (a kernel's records weigh gigabytes)
	Fixable       int    // how many of them a newer version fixes
	Search        string
}

// WorkloadGroup is one column of the target page.
type WorkloadGroup struct {
	Name    string
	Caption string // where the grouping came from, e.g. the label key
	Cards   []WorkloadCard
}

// groupModes are the ways to arrange the target page, application first.
var groupModes = []struct{ Key, Label string }{
	{"app", "Application"}, {"namespace", "Namespace"}, {"team", "Team"}, {"status", "Status"},
}

var statusColumns = []struct{ key, label string }{
	{"bad", "Needs attention"}, {"warn", "Behind"}, {"unmapped", "Not mapped"}, {"ok", "Up to date"},
}

// WorkloadCard is one workload on the target page.
type WorkloadCard struct {
	Name, Kind, Namespace string
	App, AppSource, Owner string
	ShowNamespace         bool   // when the columns are not namespaces
	Service, ServiceURL   string // empty while unmapped
	Suggested             string
	Image, Tag            string
	Resolved              string // the exact release behind a moving tag
	Running               int
	Health                string // ok, warn, bad, unmapped
	Drifts                []DriftBadge
	Upstream              string // a newer acceptable release, when there is one
	Sidecars              []string
	Search                string
}

func healthOf(drifts []DriftBadge, mapped bool) string {
	if !mapped {
		return "unmapped"
	}
	h := "ok"
	for _, d := range drifts {
		switch d.Kind {
		case "eol", "inconsistent", "exploited", "rollout":
			return "bad"
		default:
			h = "warn"
		}
	}
	return h
}

func (s *Server) targetView(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	ctx := r.Context()
	t, err := s.store.GetTarget(ctx, p.Scope, r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		s.problem(w, r, http.StatusNotFound, "Target not found", "It may have been deleted.", true)
		return nil
	}
	if err != nil {
		return err
	}
	o, err := versions.LoadOverview(ctx, s.store, p.Scope)
	if err != nil {
		return err
	}
	v := TargetDetailView{
		Base: withFlash(s.base(ctx, p, "agents", t.Name), r), ID: t.ID, Name: t.Name, Platform: t.Platform,
		Env: o.Envs[t.EnvironmentID].Name, AgentID: t.AgentID, Status: t.CollectorStatus, Error: t.CollectorError,
		LastSnapshot: t.LastSnapshotAt, CanAdmin: p.Can(store.RoleAdmin),
	}
	_, v.Stale = o.Stale[t.ID]
	for _, it := range whereItems(o.Where[t.ID]) {
		if it.Key != "platform" {
			v.Where = append(v.Where, it)
		}
	}
	v.Agent = "this server"
	if t.AgentID != "" {
		if a, err := s.store.GetAgent(ctx, p.Scope, t.AgentID); err == nil {
			v.Agent = a.Name
		}
	}

	insts, err := s.store.ListTargetInstances(ctx, p.Scope, t.ID)
	if err != nil {
		return err
	}
	type wl struct {
		main    *store.Instance
		running int
		side    []string
	}
	byWorkload := map[string]*wl{}
	var order []string
	for i := range insts {
		in := insts[i]
		if !in.RemovedAt.IsZero() {
			continue
		}
		w := byWorkload[in.WorkloadID]
		if w == nil {
			w = &wl{}
			byWorkload[in.WorkloadID] = w
			order = append(order, in.WorkloadID)
		}
		if in.IsMain {
			if w.main == nil {
				w.main = &insts[i]
			}
			w.running += in.Running
		} else {
			w.side = append(w.side, in.ContainerName+" "+in.Tag)
		}
	}
	var cards []WorkloadCard
	services := map[string]bool{}
	for _, id := range order {
		w := byWorkload[id]
		if w.main == nil {
			continue
		}
		in := w.main
		c := WorkloadCard{
			Name: in.WorkloadName, Kind: in.WorkloadKind, Namespace: in.Namespace, Image: imageRepo(in.Image), Tag: in.Tag,
			Running: w.running, Sidecars: w.side, Suggested: in.SuggestedService,
		}
		if r, ok := o.Resolved[in.Digest]; ok && in.Digest != "" && versions.IsMoving(in.Tag) {
			c.Resolved = r
		}
		running := in.Tag
		if c.Resolved != "" {
			running = c.Resolved
		}
		if svc, ok := o.Services[in.ServiceID]; ok && in.ServiceID != "" {
			c.Service, c.ServiceURL, c.Owner = svc.Name, serviceURL(svc.Name), svc.Owner
			services[svc.ID] = true
			for _, d := range o.DriftsIn(svc.ID, in.App, t.EnvironmentID) {
				c.Drifts = append(c.Drifts, driftBadge(d))
			}
			if up, ok := o.Upstreams[svc.ID]; ok && up.HasLatest {
				if cur, ok := versions.ParseVersion(running); ok && up.Latest.Compare(cur) > 0 {
					c.Upstream = up.Latest.Raw
				}
			}
		}
		c.Health = healthOf(c.Drifts, c.Service != "")
		switch c.Health {
		case "ok":
			v.OK++
		case "unmapped":
			v.Unmapped++
		default:
			v.Drifting++
		}
		words := []string{c.Name, c.Namespace, c.Service, c.Image, c.Tag, c.Resolved, c.Kind, c.Health, c.Upstream}
		for _, d := range c.Drifts {
			words = append(words, d.Label)
		}
		c.Search = strings.ToLower(strings.Join(words, " "))
		c.App, c.AppSource = in.App, in.AppSource
		cards = append(cards, c)
		v.Workloads++
	}
	v.Services = len(services)
	if t.Platform == "host" {
		v.Server = s.serverView(ctx, p, t, insts, o)
	}
	v.GroupBy = s.grouping(w, r, targetGroupCookie)
	v.GroupModes = groupModes
	if t.Platform == "host" { // a server's packages have no application or namespace
		v.GroupModes = groupModes[2:]
	}
	known := false
	for _, m := range v.GroupModes {
		known = known || m.Key == v.GroupBy
	}
	if !known {
		v.GroupBy = v.GroupModes[0].Key
		if t.Platform == "host" {
			v.GroupBy = "status"
		}
	}
	v.Groups = groupCards(cards, v.GroupBy)

	// Deploys per day over 30 days, and the latest changes.
	now := time.Now()
	evs, err := s.store.ListEvents(ctx, p.Scope, store.EventFilter{
		Types: []string{"deployed", "version_changed", "removed"}, Since: now.AddDate(0, 0, -30), Limit: 5000,
	})
	if err != nil {
		return err
	}
	v.Deploys = make([]int, 30)
	var mine []store.Event
	for _, e := range evs {
		if e.TargetID != t.ID {
			continue
		}
		mine = append(mine, e)
		if e.Type == "removed" {
			continue
		}
		d := 29 - int(now.Sub(e.At).Hours()/24)
		if d >= 0 && d < 30 {
			v.Deploys[d]++
			v.DeployMax = max(v.DeployMax, v.Deploys[d])
		}
	}
	if len(mine) > 8 {
		mine = mine[:8]
	}
	v.Changes = eventViews(mine, o)
	return render(w, r, TargetDetailPage(v))
}

// imageRepo drops the tag and digest from an image reference for display.
func imageRepo(image string) string {
	if i := strings.Index(image, "@"); i >= 0 {
		image = image[:i]
	}
	if i := strings.LastIndex(image, ":"); i > strings.LastIndex(image, "/") {
		image = image[:i]
	}
	return image
}

// ringPath is the SVG arc for a share (0…1) of a ring of radius r around (r+4, r+4).
func ringDash(share, r float64) (string, string) {
	c := 2 * math.Pi * r
	return f(c * share), f(c)
}

// percent formats a share for the ring's centre.
func percent(part, whole int) string {
	if whole == 0 {
		return "—"
	}
	return itoa(int(math.Round(float64(part)*100/float64(whole)))) + "%"
}

func share(part, whole int) float64 {
	if whole == 0 {
		return 0
	}
	return float64(part) / float64(whole)
}

// replicaDots is how many dots to draw for running replicas, and how many more.
func replicaDots(n int) (int, int) {
	if n > 10 {
		return 10, n - 10
	}
	return n, 0
}

func joinLines(s []string) string { return strings.Join(s, "\n") }

// ringClass colours the health ring by the share of current workloads.
func ringClass(ok, total int) string {
	switch sh := share(ok, total); {
	case total == 0:
		return "empty"
	case sh >= 0.8:
		return "good"
	case sh >= 0.4:
		return "fair"
	}
	return "poor"
}

// groupCards arranges workload cards in columns: by application (from labels, else
// the namespace), namespace, team (the service's owner) or status. Within a column,
// problems come first.
func groupCards(cards []WorkloadCard, by string) []WorkloadGroup {
	rank := map[string]int{"bad": 0, "warn": 1, "unmapped": 2, "ok": 3}
	byKey := map[string][]WorkloadCard{}
	sources := map[string]map[string]bool{}
	names := make([]versions.AppName, 0, len(cards))
	for _, c := range cards {
		names = append(names, versions.AppName{App: c.App, Source: c.AppSource})
	}
	fams := versions.AppFamilies(names)
	for _, c := range cards {
		c.ShowNamespace = by != "namespace" && c.Namespace != ""
		var key string
		switch by {
		case "namespace":
			key = c.Namespace
		case "team":
			key = c.Owner
			if key == "" {
				key = "no owner"
			}
		case "status":
			key = c.Health
		default:
			key = fams[c.App].Name
			if sources[key] == nil {
				sources[key] = map[string]bool{}
			}
			sources[key][c.AppSource] = true
		}
		if key == "" {
			key = "other"
		}
		byKey[key] = append(byKey[key], c)
	}
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
			if (keys[i] == "other" || keys[i] == "no owner") != (keys[j] == "other" || keys[j] == "no owner") {
				return keys[j] == "other" || keys[j] == "no owner"
			}
			return keys[i] < keys[j]
		})
	}
	out := make([]WorkloadGroup, 0, len(keys))
	for _, k := range keys {
		cs := byKey[k]
		sort.SliceStable(cs, func(i, j int) bool {
			if rank[cs[i].Health] != rank[cs[j].Health] {
				return rank[cs[i].Health] < rank[cs[j].Health]
			}
			return cs[i].Name < cs[j].Name
		})
		g := WorkloadGroup{Name: k, Cards: cs}
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
		out = append(out, g)
	}
	return out
}

// appSourceLabel says where an application's name came from, briefly.
func appSourceLabel(src string) string {
	switch src {
	case versions.SourcePinned:
		return "set by hand"
	case versions.SourceRenamed:
		return "renamed"
	case versions.SourceWorkload:
		return "workload name"
	case "app.kubernetes.io/part-of":
		return "part-of label"
	case "app.kubernetes.io/instance", "release":
		return "Helm release"
	case "com.docker.compose.project":
		return "Compose project"
	case "com.docker.stack.namespace":
		return "Swarm stack"
	case "nomad job":
		return "Nomad job"
	case "CloudFormation stack":
		return "CloudFormation stack"
	case "copilot-application":
		return "Copilot application"
	case "namespace":
		return "namespace, no app label"
	case "":
		return ""
	}
	return src + " label"
}

// osDistros are the distributions of the images a host's operating system is compared with, by repository.
var osDistros = map[string]string{
	"docker.io/library/ubuntu": "ubuntu", "docker.io/library/debian": "debian", "docker.io/library/alpine": "alpine",
	"docker.io/library/rockylinux": "rocky", "docker.io/library/almalinux": "almalinux", "docker.io/library/amazonlinux": "amzn",
}

// serverView gathers what a Linux server's page says of it: its operating system and its support, a reboot and
// package updates waiting, and its packages' vulnerabilities.
func (s *Server) serverView(ctx context.Context, p auth.Principal, t store.Target, insts []store.Instance, o versions.Overview) *ServerView {
	var osi *store.Instance
	for i := range insts {
		if insts[i].RemovedAt.IsZero() && insts[i].IsMain && insts[i].WorkloadKind == string(agentproto.HostOs) {
			osi = &insts[i]
		}
	}
	if osi == nil {
		return nil
	}
	v := &ServerView{}
	ref := versions.ParseImage(osi.Image)
	distro := osDistros[ref.Repo()]
	if distro == "" {
		distro = osi.WorkloadName
	}
	img := versions.ImageOS{Distro: distro, Version: ref.Tag}
	v.OS = strings.TrimSpace(img.String())
	if strings.HasPrefix(v.OS, " ") || v.OS == ref.Tag {
		v.OS = osi.WorkloadName + " " + ref.Tag
	}
	if svc, ok := o.Services[osi.ServiceID]; ok {
		v.OSURL = serviceURL(svc.Name)
	}
	if cy, _, ok := versions.OSCycle(ctx, img); ok && !cy.EOLFrom.IsZero() {
		v.OSEnd = cy.EOLFrom.Format(time.DateOnly)
		v.OSPast = cy.IsEOL || !cy.EOLFrom.After(time.Now())
	}
	for _, m := range osi.Maintenance {
		switch {
		case strings.HasPrefix(m, "reboot required"):
			v.Reboot = strings.TrimPrefix(strings.TrimPrefix(m, "reboot required"), ": ")
			if v.Reboot == "" {
				v.Reboot = "required"
			}
		case strings.HasPrefix(m, "package updates: "):
			v.Updates = strings.TrimPrefix(m, "package updates: ")
		}
	}
	var purls []string
	if osi.CodeDigest != "" {
		if sboms, err := s.store.ListImageSBOMs(ctx, p.Scope); err == nil {
			purls = sboms[osi.PackagesKey("")].Purls
			v.Packages = len(purls)
		}
	}
	byPackage := map[string]*ServerPackage{} // by "name version", as the report names a package
	if svc, ok := o.Services[osi.ServiceID]; ok {
		v.VulnsURL = "/security/vulns?q=" + url.QueryEscape(svc.Name)
		if rep, err := versions.LoadVulnReport(ctx, s.store, p.Scope, t.EnvironmentID); err == nil {
			for _, f := range rep.Findings {
				counted := false
				for _, a := range f.Affected {
					if !a.Host || a.ServiceID != osi.ServiceID {
						continue
					}
					if !counted {
						counted = true
						v.Vulns++
						if f.Exploited != nil {
							v.Exploited++
						}
					}
					sp := byPackage[a.Package]
					if sp == nil {
						name, version, _ := strings.Cut(a.Package, " ")
						sp = &ServerPackage{Name: name, Version: version}
						byPackage[a.Package] = sp
					}
					sp.Vulns++
					if f.Exploited != nil {
						sp.Exploited++
					}
					if severityOrder(f.Severity) > severityOrder(sp.Severity) {
						sp.Severity = f.Severity
					}
					if a.Fix != "" || a.FixNewer {
						sp.Fixable++
					}
					sp.FixNewer = sp.FixNewer || a.FixNewer
					if a.Fix != "" && (sp.Fix == "" || versions.CompareDistroVersions(a.Fix, sp.Fix) > 0) {
						sp.Fix = a.Fix // the newest fix: upgrading to it fixes every one with a fix
					}
				}
			}
			for _, u := range rep.Unsupported {
				if u.Service == svc.Name {
					v.NotChecked = u.Why
				}
			}
		}
	}
	for _, pu := range purls {
		name, version, _ := strings.Cut(versions.PackageLabel(pu), " ") // as the report names it: openssl 3.0.2-0ubuntu1.12
		if _, ok := byPackage[name+" "+version]; !ok {
			byPackage[name+" "+version] = &ServerPackage{Name: name, Version: version}
		}
	}
	for _, sp := range byPackage {
		sp.Search = strings.ToLower(sp.Name + " " + sp.Version + " " + sp.Severity)
		if sp.Exploited > 0 {
			sp.Search += " exploited"
		}
		v.PackageList = append(v.PackageList, *sp)
	}
	sort.Slice(v.PackageList, func(i, j int) bool {
		a, b := v.PackageList[i], v.PackageList[j]
		if a.Exploited != b.Exploited {
			return a.Exploited > b.Exploited
		}
		if a.Vulns != b.Vulns {
			return a.Vulns > b.Vulns
		}
		return a.Name < b.Name
	})
	return v
}

// severityOrder ranks severities for showing the worst.
func severityOrder(s string) int {
	switch strings.ToUpper(s) {
	case "CRITICAL":
		return 4
	case "HIGH":
		return 3
	case "MODERATE", "MEDIUM":
		return 2
	case "LOW":
		return 1
	}
	return 0
}

// workloadKindLabel names a workload's kind for people: a server's operating system and packages, others as they are.
func workloadKindLabel(kind string) string {
	switch kind {
	case string(agentproto.HostOs):
		return "operating system"
	case string(agentproto.HostPackage):
		return "package"
	}
	return kind
}
