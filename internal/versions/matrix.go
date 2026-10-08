// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package versions

import (
	"sort"

	"github.com/pipozzz/goliash/internal/store"
)

// Matrix is the service × environment view: what version of each service runs where.
type Matrix struct {
	Environments []store.Environment // in promotion order
	Rows         []Row
	Unmapped     int // workloads waiting in the inbox
}

// Row is one service across environments.
type Row struct {
	Service store.Service
	Cells   []Cell // one per environment, same order as Matrix.Environments
	// App is the application the service's workloads belong to (the most common one,
	// a label before the namespace fallback), and AppSource where its name came from.
	App, AppSource string
	// Parts splits the row by application when the service is used by several in one
	// environment (one postgres image, many databases). Their versions are compared
	// within each application only. Nil when the service is one application.
	Parts []Part
}

// Part is a service within one application.
type Part struct {
	App, AppSource string
	Cells          []Cell // one per environment
}

// Units are what drift is evaluated on: the row's parts, or the whole row as one
// part with no application.
func (r Row) Units() []Part {
	if len(r.Parts) > 0 {
		return r.Parts
	}
	return []Part{{Cells: r.Cells}}
}

// Cell is what runs of one service in one environment. More than one version means
// a rollout in progress or targets disagreeing.
type Cell struct {
	Versions []RunningVersion
	// Declared is what Compose files (in Git) say should run. When nothing reports
	// what actually runs, Versions holds the declared versions and FromDeclared is set.
	Declared     []RunningVersion
	FromDeclared bool
}

// Declared reports whether targets of the platform describe what should run (files in
// Git) rather than what runs.
func Declared(platform string) bool { return platform == "compose" }

// RunningVersion is one tag running in a cell.
type RunningVersion struct {
	Tag       string
	Resolved  string // the exact release a moving tag ("1", "latest") stood for, when known
	Digest    string
	Running   int
	Targets   []string // target names
	TargetIDs []string
}

// Version is the release that runs: the exact one behind a moving tag, else the tag.
func (v RunningVersion) Version() string {
	if v.Resolved != "" {
		return v.Resolved
	}
	return v.Tag
}

// Oldest is the lowest version running, the one an upgrade has to reach: a target left behind
// while most replicas moved on still needs it. Versions that do not parse count only when none does.
func (c Cell) Oldest() RunningVersion {
	oldest, found := c.Primary(), false
	var low Version
	for _, v := range c.Versions {
		pv, ok := ParseVersion(v.Version())
		if ok && (!found || pv.Compare(low) < 0) {
			oldest, low, found = v, pv, true
		}
	}
	return oldest
}

// Empty reports whether nothing of the service runs in the environment.
func (c Cell) Empty() bool { return len(c.Versions) == 0 }

// Primary is the version with most replicas.
func (c Cell) Primary() RunningVersion {
	if len(c.Versions) == 0 {
		return RunningVersion{}
	}
	return c.Versions[0]
}

// BuildMatrix assembles the matrix from active main-container instances.
func BuildMatrix(services []store.Service, envs []store.Environment, targets []store.Target, active []store.Instance) Matrix {
	m := Matrix{Environments: envs}
	envIndex := map[string]int{}
	for i, e := range envs {
		envIndex[e.ID] = i
	}
	targetName := map[string]string{}
	declared := map[string]bool{}
	for _, t := range targets {
		targetName[t.ID] = t.Name
		declared[t.ID] = Declared(t.Platform)
	}

	type cellKey struct {
		service string
		env     int
	}
	cells := map[cellKey]map[string]*RunningVersion{}
	declaredCells := map[cellKey]map[string]*RunningVersion{}
	type partKey struct {
		service, app string
		env          int
	}
	partCells := map[partKey]map[string]*RunningVersion{}
	partDeclared := map[partKey]map[string]*RunningVersion{}
	appSource := map[[2]string]string{}     // service, app -> source
	appsIn := map[cellKey]map[string]bool{} // reported apps per service and environment
	add := func(into map[string]*RunningVersion, i store.Instance) {
		v := into[i.Tag]
		if v == nil {
			v = &RunningVersion{Tag: i.Tag, Digest: i.Digest}
			into[i.Tag] = v
		}
		v.Running += i.Running
		if name := targetName[i.TargetID]; name != "" && !contains(v.Targets, name) {
			v.Targets = append(v.Targets, name)
			v.TargetIDs = append(v.TargetIDs, i.TargetID)
		}
	}
	unmapped := map[string]bool{}
	apps := map[string]map[[2]string]int{} // service -> {app, source} -> workloads
	for _, i := range active {
		if !i.IsMain {
			continue
		}
		if i.ServiceID == "" {
			unmapped[i.TargetID+"/"+i.WorkloadID] = true
			continue
		}
		if i.App != "" {
			if apps[i.ServiceID] == nil {
				apps[i.ServiceID] = map[[2]string]int{}
			}
			apps[i.ServiceID][[2]string{i.App, i.AppSource}]++
		}
		ei, ok := envIndex[i.EnvironmentID]
		if !ok {
			continue
		}
		k := cellKey{i.ServiceID, ei}
		pk := partKey{i.ServiceID, i.App, ei}
		into, intoPart := cells, partCells
		if declared[i.TargetID] {
			into, intoPart = declaredCells, partDeclared
		} else {
			if appsIn[k] == nil {
				appsIn[k] = map[string]bool{}
			}
			appsIn[k][i.App] = true
		}
		if into[k] == nil {
			into[k] = map[string]*RunningVersion{}
		}
		if intoPart[pk] == nil {
			intoPart[pk] = map[string]*RunningVersion{}
		}
		add(into[k], i)
		add(intoPart[pk], i)
		appSource[[2]string{i.ServiceID, i.App}] = i.AppSource
	}
	// A service splits by application when two of them run it in one environment.
	split := map[string]map[string]bool{} // service -> its applications
	for k, apps := range appsIn {
		if len(apps) > 1 {
			split[k.service] = map[string]bool{}
		}
	}
	for k := range partCells {
		if split[k.service] != nil {
			split[k.service][k.app] = true
		}
	}
	m.Unmapped = len(unmapped)

	for _, svc := range services {
		row := Row{Service: svc, Cells: make([]Cell, len(envs))}
		row.App, row.AppSource = commonApp(apps[svc.ID])
		present := false
		for ei := range envs {
			cell := &row.Cells[ei]
			cell.Versions = sortedVersions(cells[cellKey{svc.ID, ei}])
			cell.Declared = sortedVersions(declaredCells[cellKey{svc.ID, ei}])
			if len(cell.Versions) == 0 && len(cell.Declared) > 0 {
				cell.Versions, cell.FromDeclared = cell.Declared, true
			}
			if len(cell.Versions) > 0 {
				present = true
			}
		}
		if !present {
			continue
		}
		if apps := split[svc.ID]; apps != nil {
			names := make([]string, 0, len(apps))
			for a := range apps {
				names = append(names, a)
			}
			sort.Slice(names, func(i, j int) bool {
				if (names[i] == "") != (names[j] == "") {
					return names[j] == ""
				}
				return names[i] < names[j]
			})
			for _, a := range names {
				p := Part{App: a, AppSource: appSource[[2]string{svc.ID, a}], Cells: make([]Cell, len(envs))}
				for ei := range envs {
					cell := &p.Cells[ei]
					cell.Versions = sortedVersions(partCells[partKey{svc.ID, a, ei}])
					cell.Declared = sortedVersions(partDeclared[partKey{svc.ID, a, ei}])
					if len(cell.Versions) == 0 && len(cell.Declared) > 0 {
						cell.Versions, cell.FromDeclared = cell.Declared, true
					}
				}
				row.Parts = append(row.Parts, p)
			}
		}
		m.Rows = append(m.Rows, row)
	}
	return m
}

// commonApp picks a service's application: the one most of its workloads name by a
// label, else the most common namespace; ties go to the first name alphabetically.
func commonApp(counts map[[2]string]int) (app, source string) {
	best, bestLabel := 0, false
	for k, n := range counts {
		label := k[1] != "namespace"
		switch {
		case label != bestLabel:
			if !label {
				continue
			}
		case n < best, n == best && k[0] >= app:
			continue
		}
		app, source, best, bestLabel = k[0], k[1], n, label
	}
	return app, source
}

func sortedVersions(byTag map[string]*RunningVersion) []RunningVersion {
	vs := make([]RunningVersion, 0, len(byTag))
	for _, v := range byTag {
		sort.Strings(v.Targets)
		vs = append(vs, *v)
	}
	sort.Slice(vs, func(a, b int) bool {
		if vs[a].Running != vs[b].Running {
			return vs[a].Running > vs[b].Running
		}
		return vs[a].Tag > vs[b].Tag
	})
	if len(vs) == 0 {
		return nil
	}
	return vs
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
