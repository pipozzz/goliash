// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package ui

import (
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"

	"github.com/pipozzz/goliash/internal/auth"
)

// Applications and teams. Applications are found from workload labels (or namespaces);
// here people rename them, merge several into one, split them out again, place a
// service in one by hand, and give an application's services a team. Teams are the
// services' owners: listed, renamed, and handed services that have none.

// AppView is one application on the Applications page.
type AppView struct {
	Name     string
	From     []AppMember // the names its workloads give it, when renamed or merged
	Source   string      // where its name comes from, when not renamed
	Services []AppService
	Teams    []string
	State    string // the worst tile state of its services
	Team     string // its team, which services without one get, now and later
	OK       int    // services up to date
	Tiles    string // its card on the tiles board
}

// AppMember is a name the labels give, shown under another.
type AppMember struct{ Name string }

// AppService is a service of an application or a team.
type AppService struct {
	Name, URL, Owner, State string
	Pinned                  bool // placed in the application by hand
}

// TeamView is one team on the Teams page.
type TeamView struct {
	Name     string
	Services []AppService
	Apps     []string
	State    string
	OK       int
}

// AppsView is the Applications page.
type AppsView struct {
	Base
	Apps  []AppView
	Teams []string // for suggestions
	Names []string // application names, for suggestions
}

// TeamsView is the Teams page.
type TeamsView struct {
	Base
	Teams    []TeamView
	Unowned  []AppService
	AppNames []string
}

// appName is what an application may be called.
var appName = regexp.MustCompile(`^[\pL\pN][\pL\pN ._/-]{0,62}$`)

func (s *Server) apps(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	ctx := r.Context()
	g, err := s.overview(ctx, p.Scope, "none")
	if err != nil {
		return err
	}
	names, err := s.store.AppNames(ctx, p.Scope)
	if err != nil {
		return err
	}
	services, err := s.store.ListServices(ctx, p.Scope)
	if err != nil {
		return err
	}
	appTeams, err := s.store.AppTeams(ctx, p.Scope)
	if err != nil {
		return err
	}
	pinned := map[string]bool{}
	for _, svc := range services {
		pinned[svc.Name] = svc.App != ""
	}
	fams := rowFamilies(g.Rows)
	type acc struct {
		v       *AppView
		seen    map[string]bool
		teams   map[string]bool
		sources map[string]bool
	}
	byName := map[string]*acc{}
	var order []string
	teams := map[string]bool{}
	for _, row := range g.Rows {
		name := fams[row.App].Name
		if name == "" {
			continue // no application: listed under Teams, not here
		}
		a := byName[name]
		if a == nil {
			a = &acc{v: &AppView{Name: name, State: stNone, Tiles: "/tiles?group=app&app=" + url.QueryEscape(name)}, seen: map[string]bool{}, teams: map[string]bool{}, sources: map[string]bool{}}
			byName[name] = a
			order = append(order, name)
		}
		a.sources[row.AppSource] = true
		if row.Owner != "" {
			a.teams[row.Owner], teams[row.Owner] = true, true
		}
		if a.seen[row.Service] {
			continue
		}
		a.seen[row.Service] = true
		c, _, _ := service(row, g.Envs, "")
		a.v.Services = append(a.v.Services, AppService{Name: row.Service, URL: row.URL, Owner: row.Owner, State: c.State, Pinned: pinned[row.Service]})
		a.v.State = worse(a.v.State, c.State)
		if c.State == stCurrent {
			a.v.OK++
		}
	}
	for from, to := range names {
		if a := byName[to]; a != nil {
			a.v.From = append(a.v.From, AppMember{Name: from})
		}
	}
	sort.Strings(order)
	v := AppsView{Base: withFlash(s.base(ctx, p, "apps", "Applications"), r)}
	for _, name := range order {
		a := byName[name]
		sort.Slice(a.v.From, func(i, j int) bool { return a.v.From[i].Name < a.v.From[j].Name })
		if len(a.v.From) == 0 && len(a.sources) == 1 {
			for src := range a.sources {
				a.v.Source = appSourceLabel(src)
			}
		}
		for t := range a.teams {
			a.v.Teams = append(a.v.Teams, t)
		}
		a.v.Team = appTeams[name]
		sort.Strings(a.v.Teams)
		v.Apps = append(v.Apps, *a.v)
		v.Names = append(v.Names, name)
	}
	for t := range teams {
		v.Teams = append(v.Teams, t)
	}
	sort.Strings(v.Teams)
	return render(w, r, AppsPage(v))
}

// renameApp renames or merges an application ("to" an existing one merges), or
// splits a name out again ("to" empty).
func (s *Server) renameApp(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	ctx := r.Context()
	from, to := strings.TrimSpace(r.FormValue("app")), strings.TrimSpace(r.FormValue("to"))
	if from == "" || (to != "" && !appName.MatchString(to)) {
		return back(w, r, "/apps", "error", "Give the application a name: letters, digits, spaces, dots, dashes, slashes.")
	}
	if err := s.store.RenameApp(ctx, p.Scope, from, to); err != nil {
		return err
	}
	s.reevaluate(r, p)
	s.audit(ctx, p, "app.rename", "app", from, "to", to)
	if to == "" || to == from {
		return back(w, r, "/apps", "notice", from+" goes by the name its labels give again.")
	}
	return back(w, r, "/apps", "notice", from+" is shown as "+to+" now.")
}

// appTeam gives every service of an application a team.
func (s *Server) appTeam(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	ctx := r.Context()
	app, team := strings.TrimSpace(r.FormValue("app")), strings.TrimSpace(r.FormValue("team"))
	g, err := s.overview(ctx, p.Scope, "none")
	if err != nil {
		return err
	}
	fams := rowFamilies(g.Rows)
	var ids []string
	for _, row := range g.Rows {
		if fams[row.App].Name != app {
			continue
		}
		if svc, err := s.store.GetServiceByName(ctx, p.Scope, row.Service); err == nil {
			ids = append(ids, svc.ID)
		}
	}
	if err := s.store.SetAppTeam(ctx, p.Scope, app, team); err != nil {
		return err
	}
	if team == "" {
		s.audit(ctx, p, "app.team", "app", app, "team", "")
		return back(w, r, "/apps", "notice", "New services in "+app+" no longer get a team; the services keep theirs.")
	}
	n, err := s.store.SetOwners(ctx, p.Scope, ids, team)
	if err != nil {
		return err
	}
	s.hub.Publish(p.Scope.WorkspaceID)
	s.audit(ctx, p, "app.team", "app", app, "team", team, "services", itoa(n))
	return back(w, r, "/apps", "notice", plural(n, "service", "services")+" of "+app+" now belong to "+team+"; services that appear in it later get it too.")
}

func (s *Server) teams(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	ctx := r.Context()
	g, err := s.overview(ctx, p.Scope, "none")
	if err != nil {
		return err
	}
	fams := rowFamilies(g.Rows)
	byTeam := map[string]*TeamView{}
	apps := map[string]map[string]bool{}
	seen := map[string]bool{}
	v := TeamsView{Base: withFlash(s.base(ctx, p, "teams", "Teams"), r)}
	appSet := map[string]bool{}
	for _, row := range g.Rows {
		if n := fams[row.App].Name; n != "" {
			appSet[n] = true
		}
		key := row.Owner + "|" + row.Service
		if seen[key] {
			continue
		}
		seen[key] = true
		c, _, _ := service(row, g.Envs, "")
		svc := AppService{Name: row.Service, URL: row.URL, Owner: row.Owner, State: c.State}
		if row.Owner == "" {
			v.Unowned = append(v.Unowned, svc)
			continue
		}
		t := byTeam[row.Owner]
		if t == nil {
			t = &TeamView{Name: row.Owner, State: stNone}
			byTeam[row.Owner], apps[row.Owner] = t, map[string]bool{}
		}
		t.Services = append(t.Services, svc)
		t.State = worse(t.State, c.State)
		if c.State == stCurrent {
			t.OK++
		}
		if n := fams[row.App].Name; n != "" {
			apps[row.Owner][n] = true
		}
	}
	for name, t := range byTeam {
		for a := range apps[name] {
			t.Apps = append(t.Apps, a)
		}
		sort.Strings(t.Apps)
		v.Teams = append(v.Teams, *t)
	}
	sort.Slice(v.Teams, func(i, j int) bool { return v.Teams[i].Name < v.Teams[j].Name })
	for a := range appSet {
		v.AppNames = append(v.AppNames, a)
	}
	sort.Strings(v.AppNames)
	return render(w, r, TeamsPage(v))
}

// renameTeam renames a team on all its services.
func (s *Server) renameTeam(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	ctx := r.Context()
	from, to := strings.TrimSpace(r.FormValue("team")), strings.TrimSpace(r.FormValue("to"))
	if from == "" || to == "" {
		return back(w, r, "/teams", "error", "Give the team a new name.")
	}
	n, err := s.store.RenameOwner(ctx, p.Scope, from, to)
	if err != nil {
		return err
	}
	s.hub.Publish(p.Scope.WorkspaceID)
	s.audit(ctx, p, "team.rename", "team", from, "to", to, "services", itoa(n))
	return back(w, r, "/teams", "notice", from+" is "+to+" now, on "+plural(n, "service", "services")+". Notification rules filtering by the old name need the new one.")
}

// assignTeam gives the picked services a team.
func (s *Server) assignTeam(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	ctx := r.Context()
	if err := r.ParseForm(); err != nil {
		return err
	}
	team := strings.TrimSpace(r.FormValue("team"))
	if team == "" {
		return back(w, r, "/teams", "error", "Name the team.")
	}
	var ids []string
	for _, name := range r.Form["service"] {
		if svc, err := s.store.GetServiceByName(ctx, p.Scope, name); err == nil {
			ids = append(ids, svc.ID)
		}
	}
	if len(ids) == 0 {
		return back(w, r, "/teams", "error", "Pick the services first.")
	}
	n, err := s.store.SetOwners(ctx, p.Scope, ids, team)
	if err != nil {
		return err
	}
	s.hub.Publish(p.Scope.WorkspaceID)
	s.audit(ctx, p, "team.assign", "team", team, "services", itoa(n))
	return back(w, r, "/teams", "notice", plural(n, "service", "services")+" now belong to "+team+".")
}

// reevaluate recomputes drift after applications changed, so it follows the new names.
func (s *Server) reevaluate(r *http.Request, p auth.Principal) {
	if s.checker != nil {
		_ = s.checker.EvaluateDrift(r.Context(), p.Scope)
	}
	s.hub.Publish(p.Scope.WorkspaceID)
}

func teamNames(ts []TeamView) []string {
	out := make([]string, len(ts))
	for i, t := range ts {
		out[i] = t.Name
	}
	return out
}

func joinComma(s []string) string { return strings.Join(s, ", ") }
