// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package versions

import (
	"context"
	"sort"
	"time"

	"github.com/pipozzz/goliash/internal/store"
)

// OverviewAt is the matrix as it was at a point in time, from the instance history.
// It carries no drift or upstream data: those describe now, not then.
func OverviewAt(ctx context.Context, st *store.Store, sc store.Scope, at time.Time) (Overview, error) {
	services, err := st.ListServices(ctx, sc)
	if err != nil {
		return Overview{}, err
	}
	envs, err := st.ListEnvironments(ctx, sc)
	if err != nil {
		return Overview{}, err
	}
	targets, err := st.ListTargets(ctx, sc)
	if err != nil {
		return Overview{}, err
	}
	instances, err := st.InstancesAt(ctx, sc, at)
	if err != nil {
		return Overview{}, err
	}
	o := Overview{
		Matrix: BuildMatrix(services, envs, targets, instances), Targets: targets,
		Services: map[string]store.Service{}, Envs: map[string]store.Environment{},
	}
	if o.Resolved, err = resolvedDigests(ctx, st, sc); err != nil {
		return Overview{}, err
	}
	applyResolutions(&o.Matrix, o.Resolved)
	o.SplitApps = splitApps(o.Matrix)
	for _, s := range services {
		o.Services[s.ID] = s
	}
	for _, e := range envs {
		o.Envs[e.ID] = e
	}
	return o, nil
}

// InventoryItem is one running container, for audits and exports.
type InventoryItem struct {
	Environment string    `json:"environment"`
	Target      string    `json:"target"`
	Platform    string    `json:"platform"`
	Service     string    `json:"service,omitempty"`
	Namespace   string    `json:"namespace,omitempty"`
	Workload    string    `json:"workload"`
	Kind        string    `json:"kind"`
	Container   string    `json:"container"`
	Image       string    `json:"image"`
	Tag         string    `json:"tag,omitempty"`
	Digest      string    `json:"digest,omitempty"`
	Running     int       `json:"running"`
	FirstSeen   time.Time `json:"first_seen"`
}

// Inventory lists every container that runs now, or ran at a point in time, sidecars
// included, sorted by environment, target and workload.
func Inventory(ctx context.Context, st *store.Store, sc store.Scope, at *time.Time) ([]InventoryItem, error) {
	var instances []store.Instance
	var err error
	if at != nil {
		instances, err = st.InstancesAt(ctx, sc, *at)
	} else {
		instances, err = st.ListActiveInstances(ctx, sc)
	}
	if err != nil {
		return nil, err
	}
	envs, err := st.ListEnvironments(ctx, sc)
	if err != nil {
		return nil, err
	}
	targets, err := st.ListTargets(ctx, sc)
	if err != nil {
		return nil, err
	}
	services, err := st.ListServices(ctx, sc)
	if err != nil {
		return nil, err
	}
	envName, svcName := map[string]string{}, map[string]string{}
	envPos := map[string]int{}
	for _, e := range envs {
		envName[e.ID], envPos[e.Name] = e.Name, e.Position
	}
	for _, s := range services {
		svcName[s.ID] = s.Name
	}
	target := map[string]store.Target{}
	for _, t := range targets {
		target[t.ID] = t
	}
	out := make([]InventoryItem, 0, len(instances))
	for _, i := range instances {
		t := target[i.TargetID]
		out = append(out, InventoryItem{
			Environment: envName[i.EnvironmentID], Target: t.Name, Platform: t.Platform, Service: svcName[i.ServiceID],
			Namespace: i.Namespace, Workload: i.WorkloadName, Kind: i.WorkloadKind, Container: i.ContainerName,
			Image: i.Image, Tag: i.Tag, Digest: i.Digest, Running: i.Running, FirstSeen: i.FirstSeenAt,
		})
	}
	sort.Slice(out, func(a, b int) bool {
		x, y := out[a], out[b]
		if x.Environment != y.Environment {
			return envPos[x.Environment] < envPos[y.Environment]
		}
		if x.Target != y.Target {
			return x.Target < y.Target
		}
		if x.Workload != y.Workload {
			return x.Workload < y.Workload
		}
		return x.Container < y.Container
	})
	return out, nil
}
