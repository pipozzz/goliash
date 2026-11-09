// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package versions

import (
	"context"
	"regexp"
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
	"nomad job": true, "namespace": true, "compose project": true, "com.docker.compose.project": true, "com.docker.stack.namespace": true,
	SourceWorkload: true,
	// Stacks are often one per function or per developer (shop-api-dev-ana, shop-api-dev-bo): a family per first
	// word keeps a region's functions from being hundreds of applications.
	"CloudFormation stack": true,
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
	splitByWorkload(instances)
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

// CodeOwnersTeam is the team a repository's CODEOWNERS gives a path on a branch, "" for none.
type CodeOwnersTeam func(ctx context.Context, repo, branch, path string) (string, error)

// OwnerSources are where owners come from besides labels and applications; each may be nil.
type OwnerSources struct {
	CodeOwners CodeOwnersTeam
	Catalog    func(ctx context.Context) ([]CatalogComponent, error) // a Backstage catalog
}

// FillOwners gives services the team Goliash can tell for them, without overriding
// people: a service's owner set by hand stays; otherwise the team its workloads'
// labels name (goliash.team, team, owner) wins, then the team of its Backstage
// component, then the team CODEOWNERS gives the path its GitOps application deploys
// from, else its application's team. Owners Goliash set follow later changes of these.
// A service whose sources disagree is left alone. It returns what it set.
func FillOwners(ctx context.Context, st *store.Store, sc store.Scope, src OwnerSources) ([]string, error) {
	services, err := st.ListServices(ctx, sc)
	if err != nil {
		return nil, err
	}
	active, err := st.ListActiveInstances(ctx, sc)
	if err != nil {
		return nil, err
	}
	labelTeams := map[string]map[string]bool{}
	for _, in := range active {
		if in.Team != "" && in.ServiceID != "" {
			if labelTeams[in.ServiceID] == nil {
				labelTeams[in.ServiceID] = map[string]bool{}
			}
			labelTeams[in.ServiceID][in.Team] = true
		}
	}
	ownedTeams, err := codeOwnerTeams(ctx, st, sc, active, src.CodeOwners)
	if err != nil {
		return nil, err
	}
	catalogTeams, err := catalogTeams(ctx, services, active, src.Catalog)
	if err != nil {
		return nil, err
	}
	teams, err := st.AppTeams(ctx, sc)
	if err != nil {
		return nil, err
	}
	var apps map[string][]string
	if len(teams) > 0 {
		if apps, err = ServiceApps(ctx, st, sc); err != nil {
			return nil, err
		}
	}
	var named []string
	for _, svc := range services {
		if svc.Owner != "" && svc.OwnerSource == "" {
			continue // set by people
		}
		team, source := "", ""
		switch lt := labelTeams[svc.ID]; {
		case len(lt) == 1:
			for t := range lt {
				team, source = t, "label"
			}
		case len(lt) > 1:
			continue // its workloads name different teams: leave it to people
		case len(catalogTeams[svc.ID]) == 1:
			for t := range catalogTeams[svc.ID] {
				team, source = t, "backstage"
			}
		case len(catalogTeams[svc.ID]) > 1:
			continue
		case len(ownedTeams[svc.ID]) == 1:
			for t := range ownedTeams[svc.ID] {
				team, source = t, "codeowners"
			}
		case len(ownedTeams[svc.ID]) > 1:
			continue
		default:
			team, source = agreedTeam(apps[svc.ID], teams), "app"
		}
		if team == "" || (team == svc.Owner && source == svc.OwnerSource) {
			continue
		}
		if _, err := st.SetOwners(ctx, sc, []string{svc.ID}, team, source); err != nil {
			return named, err
		}
		named = append(named, svc.Name+" → "+team+" ("+source+")")
	}
	return named, nil
}

// catalogTeams is, by service, the teams owning the Backstage components that are it: by the goliash.dev/service
// annotation, else by the component's name, or by backstage.io/kubernetes-id naming one of its workloads. A catalog
// that cannot be read is skipped until the next check.
func catalogTeams(ctx context.Context, services []store.Service, active []store.Instance, catalog func(context.Context) ([]CatalogComponent, error)) (map[string]map[string]bool, error) {
	if catalog == nil {
		return nil, nil
	}
	comps, err := catalog(ctx)
	if err != nil || len(comps) == 0 {
		return nil, ctx.Err()
	}
	byName, byWorkload := map[string]string{}, map[string]map[string]bool{}
	for _, s := range services {
		byName[strings.ToLower(s.Name)] = s.ID
	}
	for _, in := range active {
		if in.ServiceID != "" && in.WorkloadName != "" {
			w := strings.ToLower(in.WorkloadName)
			if byWorkload[w] == nil {
				byWorkload[w] = map[string]bool{}
			}
			byWorkload[w][in.ServiceID] = true
		}
	}
	out := map[string]map[string]bool{}
	add := func(id, team string) {
		if out[id] == nil {
			out[id] = map[string]bool{}
		}
		out[id][team] = true
	}
	for _, c := range comps {
		if c.Team == "" {
			continue
		}
		name := c.Service
		if name == "" {
			name = c.Name
		}
		if id, ok := byName[strings.ToLower(name)]; ok {
			add(id, c.Team)
			continue
		}
		if c.Service == "" && c.KubernetesID != "" {
			for id := range byWorkload[strings.ToLower(c.KubernetesID)] {
				add(id, c.Team)
			}
		}
	}
	return out, nil
}

// codeOwnerTeams is, by service, the teams CODEOWNERS gives the paths its workloads' GitOps applications deploy
// from. A repository that cannot be read is skipped: CODEOWNERS only adds owners, and the next check tries again.
func codeOwnerTeams(ctx context.Context, st *store.Store, sc store.Scope, active []store.Instance, codeOwners CodeOwnersTeam) (map[string]map[string]bool, error) {
	if codeOwners == nil {
		return nil, nil
	}
	apps, err := st.ListWorkloadGitOps(ctx, sc)
	if err != nil || len(apps) == 0 {
		return nil, err
	}
	byWorkload := map[string]store.WorkloadGitOps{}
	for _, a := range apps {
		byWorkload[a.TargetID+"|"+a.WorkloadID] = a
	}
	out := map[string]map[string]bool{}
	asked := map[string]string{}
	for _, in := range active {
		a, ok := byWorkload[in.TargetID+"|"+in.WorkloadID]
		repo := RepoWebURL(a.Repo) // "" for a Helm chart repository
		if in.ServiceID == "" || !ok || repo == "" {
			continue
		}
		key := repo + "@" + a.TargetRevision + ":" + a.Path
		team, seen := asked[key]
		if !seen {
			if team, err = codeOwners(ctx, repo, a.TargetRevision, a.Path); err != nil {
				if ctx.Err() != nil {
					return nil, ctx.Err()
				}
				team = ""
			}
			asked[key] = team
		}
		if team != "" {
			if out[in.ServiceID] == nil {
				out[in.ServiceID] = map[string]bool{}
			}
			out[in.ServiceID][team] = true
		}
	}
	return out, nil
}

// agreedTeam is the team of a service's applications when they agree on one.
func agreedTeam(apps []string, teams map[string]string) string {
	team := ""
	for _, a := range apps {
		t := teams[a]
		if t == "" {
			continue
		}
		if team != "" && t != team {
			return ""
		}
		team = t
	}
	return team
}

// SourceWorkload marks an application named after the workload: several different
// workloads (databases of different projects, say) run one service's image with
// nothing else telling them apart.
const SourceWorkload = "workload name"

// splitByWorkload tells apart workloads of one service that nothing else does: when a
// service runs under two or more workload names (generated suffixes aside) within an
// application that only comes from a namespace, or none, each workload name becomes
// its application. So goliash-db and cefiro-db, both postgres, are compared each on
// its own instead of as one inconsistent service. Labels and people's choices win.
func splitByWorkload(instances []store.Instance) {
	weak := func(in store.Instance) bool { return in.App == "" || in.AppSource == "namespace" }
	names := map[string]map[string]bool{} // service|app -> stable workload names
	for _, in := range instances {
		if in.ServiceID == "" || !weak(in) {
			continue
		}
		k := in.ServiceID + "|" + in.App
		if names[k] == nil {
			names[k] = map[string]bool{}
		}
		names[k][StableName(in.WorkloadName)] = true
	}
	for i := range instances {
		in := &instances[i]
		if in.ServiceID == "" || !weak(*in) || len(names[in.ServiceID+"|"+in.App]) < 2 {
			continue
		}
		in.App, in.AppSource = StableName(in.WorkloadName), SourceWorkload
	}
}

var generatedSuffix = regexp.MustCompile(`^[a-z0-9]{6}$`)

// StableName drops the random suffix platforms such as Nomad, Dokploy and Nomploy add
// to the names they generate, "<project>-<service>-<6 random>" (cefiro-db-wruzyw ->
// cefiro-db), so a workload keeps one name across redeploys. Names with fewer than
// three parts are left alone: "code-server" has no suffix to drop.
func StableName(name string) string {
	parts := strings.Split(name, "-")
	if len(parts) < 3 || !generatedSuffix.MatchString(parts[len(parts)-1]) {
		return name
	}
	return strings.Join(parts[:len(parts)-1], "-")
}
