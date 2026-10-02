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
}

// Cell is what runs of one service in one environment. More than one version means
// a rollout in progress or targets disagreeing.
type Cell struct {
	Versions []RunningVersion
}

// RunningVersion is one tag running in a cell.
type RunningVersion struct {
	Tag       string
	Digest    string
	Running   int
	Targets   []string // target names
	TargetIDs []string
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
	for _, t := range targets {
		targetName[t.ID] = t.Name
	}

	type cellKey struct {
		service string
		env     int
	}
	cells := map[cellKey]map[string]*RunningVersion{}
	unmapped := map[string]bool{}
	for _, i := range active {
		if !i.IsMain {
			continue
		}
		if i.ServiceID == "" {
			unmapped[i.TargetID+"/"+i.WorkloadID] = true
			continue
		}
		ei, ok := envIndex[i.EnvironmentID]
		if !ok {
			continue
		}
		k := cellKey{i.ServiceID, ei}
		if cells[k] == nil {
			cells[k] = map[string]*RunningVersion{}
		}
		v := cells[k][i.Tag]
		if v == nil {
			v = &RunningVersion{Tag: i.Tag, Digest: i.Digest}
			cells[k][i.Tag] = v
		}
		v.Running += i.Running
		if name := targetName[i.TargetID]; name != "" && !contains(v.Targets, name) {
			v.Targets = append(v.Targets, name)
			v.TargetIDs = append(v.TargetIDs, i.TargetID)
		}
	}
	m.Unmapped = len(unmapped)

	for _, svc := range services {
		row := Row{Service: svc, Cells: make([]Cell, len(envs))}
		present := false
		for ei := range envs {
			for _, v := range cells[cellKey{svc.ID, ei}] {
				sort.Strings(v.Targets)
				row.Cells[ei].Versions = append(row.Cells[ei].Versions, *v)
				present = true
			}
			vs := row.Cells[ei].Versions
			sort.Slice(vs, func(a, b int) bool {
				if vs[a].Running != vs[b].Running {
					return vs[a].Running > vs[b].Running
				}
				return vs[a].Tag > vs[b].Tag
			})
		}
		if present {
			m.Rows = append(m.Rows, row)
		}
	}
	return m
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
