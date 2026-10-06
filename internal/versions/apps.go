// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package versions

import (
	"context"
	"sort"
	"strings"

	"github.com/pipozzz/goliash/internal/store"
)

// AppName is an application as stored with a workload, and where the name came from.
type AppName struct{ App, Source string }

// AppFamily is what an application is shown under: its project, when platforms
// like Dokploy or Nomploy deployed its parts as separate apps named
// "<project>-<part>" (cefiro, cefiro-db, cefiro-redis -> cefiro), else itself.
type AppFamily struct {
	Name    string
	Members []string // the applications merged into it, when more than one
}

// derivedSources are the app sources that come from generated names, not from a
// label someone chose: only those are merged. An explicit label stays as it is.
var derivedSources = map[string]bool{
	"nomad job": true, "namespace": true, "com.docker.compose.project": true, "com.docker.stack.namespace": true,
}

// AppFamilies maps each application to its family. Applications from generated
// names whose first word is shared with another application ("goliash-db" and the
// "goliash" job) are merged under that word.
func AppFamilies(apps []AppName) map[string]AppFamily {
	byFirst := map[string]map[string]bool{}
	for _, a := range apps {
		if a.App == "" || !derivedSources[a.Source] {
			continue
		}
		first, _, _ := strings.Cut(a.App, "-")
		if byFirst[first] == nil {
			byFirst[first] = map[string]bool{}
		}
		byFirst[first][a.App] = true
	}
	out := map[string]AppFamily{}
	for _, a := range apps {
		if _, done := out[a.App]; done || a.App == "" {
			continue
		}
		out[a.App] = AppFamily{Name: a.App}
		if !derivedSources[a.Source] {
			continue
		}
		first, _, _ := strings.Cut(a.App, "-")
		if members := byFirst[first]; len(members) > 1 {
			names := make([]string, 0, len(members))
			for m := range members {
				names = append(names, m)
			}
			sort.Strings(names)
			out[a.App] = AppFamily{Name: first, Members: names}
		}
	}
	return out
}

// App sources set by people rather than found in labels: a service placed in an
// application by hand, and an application renamed or merged.
const (
	SourcePinned  = "set by hand"
	SourceRenamed = "renamed"
)

// NameApps applies what people decided about applications to instances: a service
// placed in one by hand goes there, and a renamed or merged application takes its
// new name. Both are explicit, so neither is merged into a family afterwards.
func NameApps(instances []store.Instance, services []store.Service, names map[string]string) {
	pinned := map[string]string{}
	for _, s := range services {
		if s.App != "" {
			pinned[s.ID] = s.App
		}
	}
	if len(pinned) == 0 && len(names) == 0 {
		return
	}
	for i := range instances {
		in := &instances[i]
		if a := pinned[in.ServiceID]; a != "" {
			if to, ok := names[a]; ok {
				a = to // a renamed application keeps the services placed in it
			}
			in.App, in.AppSource = a, SourcePinned
			continue
		}
		if to, ok := names[in.App]; ok && in.App != "" {
			in.App, in.AppSource = to, SourceRenamed
		}
	}
}

// namedInstances lists instances with people's application names applied.
func namedInstances(ctx context.Context, st *store.Store, sc store.Scope, instances []store.Instance, services []store.Service) ([]store.Instance, error) {
	names, err := st.AppNames(ctx, sc)
	if err != nil {
		return nil, err
	}
	NameApps(instances, services, names)
	return instances, nil
}

// ServiceApps lists the applications each service runs in, by service ID, as the
// matrix shows them: renames, merges, services placed by hand and families applied.
func ServiceApps(ctx context.Context, st *store.Store, sc store.Scope) (map[string][]string, error) {
	services, err := st.ListServices(ctx, sc)
	if err != nil {
		return nil, err
	}
	active, err := st.ListActiveInstances(ctx, sc)
	if err != nil {
		return nil, err
	}
	if active, err = namedInstances(ctx, st, sc, active, services); err != nil {
		return nil, err
	}
	var names []AppName
	for _, in := range active {
		if in.App != "" {
			names = append(names, AppName{App: in.App, Source: in.AppSource})
		}
	}
	fams := AppFamilies(names)
	seen := map[string]bool{}
	out := map[string][]string{}
	for _, in := range active {
		if in.App == "" || in.ServiceID == "" {
			continue
		}
		name := fams[in.App].Name
		if name == "" {
			name = in.App
		}
		if k := in.ServiceID + "|" + name; !seen[k] {
			seen[k] = true
			out[in.ServiceID] = append(out[in.ServiceID], name)
		}
	}
	for id := range out {
		sort.Strings(out[id])
	}
	return out, nil
}

// FamilyOf returns the name an application shows under, given its service's applications.
func FamilyOf(app string, apps []string) string {
	for _, a := range apps {
		if a == app || strings.HasPrefix(app, a+"-") {
			return a
		}
	}
	return app
}

// FillOwners gives services without an owner the team of their application, when
// their applications agree on one. It returns the services it gave a team.
func FillOwners(ctx context.Context, st *store.Store, sc store.Scope) ([]string, error) {
	teams, err := st.AppTeams(ctx, sc)
	if err != nil || len(teams) == 0 {
		return nil, err
	}
	services, err := st.ListServices(ctx, sc)
	if err != nil {
		return nil, err
	}
	apps, err := ServiceApps(ctx, st, sc)
	if err != nil {
		return nil, err
	}
	var named []string
	for _, svc := range services {
		if svc.Owner != "" {
			continue
		}
		team := ""
		for _, a := range apps[svc.ID] {
			t := teams[a]
			if t == "" {
				continue
			}
			if team != "" && t != team {
				team = "" // its applications disagree: leave it to people
				break
			}
			team = t
		}
		if team == "" {
			continue
		}
		if _, err := st.SetOwners(ctx, sc, []string{svc.ID}, team); err != nil {
			return named, err
		}
		named = append(named, svc.Name+" → "+team)
	}
	return named, nil
}
