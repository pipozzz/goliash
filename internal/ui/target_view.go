// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package ui

import (
	"errors"
	"math"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/pipozzz/goliash/internal/auth"
	"github.com/pipozzz/goliash/internal/store"
	"github.com/pipozzz/goliash/internal/versions"
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

	Workloads, Services, OK, Drifting, Unmapped int
	Groups                                      []WorkloadGroup
	GroupBy                                     string // app, namespace, team or status
	Deploys                                     []int  // per day, oldest first
	DeployMax                                   int
	Changes                                     []EventView
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
		case "eol", "inconsistent":
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
	v.GroupBy = s.grouping(w, r, targetGroupCookie)
	known := false
	for _, m := range groupModes {
		known = known || m.Key == v.GroupBy
	}
	if !known {
		v.GroupBy = "app"
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
	case "namespace":
		return "namespace, no app label"
	case "":
		return ""
	}
	return src + " label"
}
