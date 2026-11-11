// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package ui

import (
	"net/http"
	"sort"
	"strings"

	"github.com/pipozzz/goliash/internal/auth"
	"github.com/pipozzz/goliash/internal/versions"
	"github.com/pipozzz/goliash/pkg/agentproto"
)

// InfraView is the infrastructure map: where everything runs, cloud by cloud, account by account, region by
// region, each target a tile coloured by how current what it runs is.
type InfraView struct {
	Base
	Clouds  []InfraCloud
	Targets int
	Bad     int // targets with something to look at
	Stale   int
}

// InfraCloud is a cloud (or the machines outside one) and its accounts.
type InfraCloud struct {
	Name     string // AWS, Azure, Google Cloud, Outside a cloud
	Accounts []InfraAccount
}

// InfraAccount is an account (a subscription, a project) and its regions.
type InfraAccount struct {
	Name    string
	Known   bool // an account ID, not a stand-in
	Regions []InfraRegion
	Bad     int
}

// InfraRegion is a region and its targets.
type InfraRegion struct {
	Name    string
	Targets []InfraTarget
}

// InfraTarget is one target on the map.
type InfraTarget struct {
	ID, Name, Platform, Env string
	Kind                    string // the platform's label
	Icon                    string // the platform's icon path
	Workloads               int
	OK, Warn, Bad, Unmapped int
	Stale                   bool
	Reboot                  bool   // a server waiting for a reboot
	Health                  string // ok, warn, bad, unmapped, empty
	Title                   string
	Search                  string // what the filter matches: name, environment, platform, health and where
}

var cloudNames = map[string]string{"aws": "AWS", "azure": "Azure", "gcp": "Google Cloud", "": "Outside a cloud"}

// infrastructure draws the map.
func (s *Server) infrastructure(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	ctx := r.Context()
	o, err := versions.LoadOverview(ctx, s.store, p.Scope)
	if err != nil {
		return err
	}
	active, err := s.store.ListActiveInstances(ctx, p.Scope)
	if err != nil {
		return err
	}
	type key struct{ cloud, account, region string }
	tiles := map[key][]InfraTarget{}
	v := InfraView{Base: s.base(ctx, p, "infrastructure", "Infrastructure")}
	byTarget := map[string]*InfraTarget{}
	for _, t := range o.Targets {
		where := o.Where[t.ID]
		it := InfraTarget{ID: t.ID, Name: t.Name, Platform: t.Platform, Env: o.Envs[t.EnvironmentID].Name, Kind: platformLabel(t.Platform)}
		if k, ok := kindOf(t.Platform); ok {
			it.Icon = k.Icon
		}
		_, it.Stale = o.Stale[t.ID]
		k := key{where["cloud"], where["account"], where["region"]}
		it.Search = strings.ToLower(strings.Join([]string{t.Name, it.Env, it.Kind, t.Platform, k.cloud, k.account, k.region, where["cluster"]}, " "))
		tiles[k] = append(tiles[k], it)
		v.Targets++
	}
	for k, list := range tiles {
		for i := range list {
			byTarget[list[i].ID] = &tiles[k][i]
		}
	}
	seen := map[string]bool{}
	for _, in := range active {
		it := byTarget[in.TargetID]
		if it == nil || !in.IsMain || !in.RemovedAt.IsZero() || seen[in.WorkloadID] {
			continue
		}
		seen[in.WorkloadID] = true
		it.Workloads++
		if in.WorkloadKind == string(agentproto.HostOs) {
			for _, m := range in.Maintenance {
				it.Reboot = it.Reboot || strings.HasPrefix(m, "reboot required")
			}
		}
		if _, ok := o.Services[in.ServiceID]; !ok || in.ServiceID == "" {
			it.Unmapped++
			continue
		}
		var drifts []DriftBadge
		for _, d := range o.DriftsIn(in.ServiceID, in.App, in.EnvironmentID) {
			drifts = append(drifts, driftBadge(d))
		}
		switch healthOf(drifts, true) {
		case "bad":
			it.Bad++
		case "warn":
			it.Warn++
		default:
			it.OK++
		}
	}
	for _, it := range byTarget {
		switch {
		case it.Workloads == 0:
			it.Health = "empty"
		case it.Bad > 0:
			it.Health = "bad"
		case it.Warn > 0:
			it.Health = "warn"
		case it.Unmapped > 0:
			it.Health = "unmapped"
		default:
			it.Health = "ok"
		}
		it.Title = it.Name + ": " + itoa(it.OK) + " up to date, " + itoa(it.Warn) + " behind, " + itoa(it.Bad) + " needing attention"
		if it.Unmapped > 0 {
			it.Title += ", " + itoa(it.Unmapped) + " not mapped"
		}
		it.Search += " " + it.Health
		if it.Stale {
			it.Search += " stale"
		}
		if it.Reboot {
			it.Search += " reboot"
		}
		if it.Bad > 0 {
			v.Bad++
		}
		if it.Stale {
			v.Stale++
		}
	}
	clouds := map[string]map[string]map[string][]InfraTarget{}
	for k, list := range tiles {
		if clouds[k.cloud] == nil {
			clouds[k.cloud] = map[string]map[string][]InfraTarget{}
		}
		if clouds[k.cloud][k.account] == nil {
			clouds[k.cloud][k.account] = map[string][]InfraTarget{}
		}
		clouds[k.cloud][k.account][k.region] = append(clouds[k.cloud][k.account][k.region], list...)
	}
	for _, c := range sortedMapKeys(clouds) {
		ic := InfraCloud{Name: cloudNames[c]}
		if ic.Name == "" {
			ic.Name = c
		}
		for _, a := range sortedMapKeys(clouds[c]) {
			ia := InfraAccount{Name: a, Known: a != ""}
			if a == "" {
				ia.Name = "no account known"
				if c == "" {
					ia.Name = "Servers and clusters"
				}
			}
			for _, rg := range sortedMapKeys(clouds[c][a]) {
				list := clouds[c][a][rg]
				sort.Slice(list, func(i, j int) bool {
					if healthRank[list[i].Health] != healthRank[list[j].Health] {
						return healthRank[list[i].Health] < healthRank[list[j].Health]
					}
					return list[i].Name < list[j].Name
				})
				name := rg
				if name == "" && len(clouds[c][a]) > 1 {
					name = "region not known"
				}
				for _, t := range list {
					if t.Health == "bad" {
						ia.Bad++
					}
				}
				ia.Regions = append(ia.Regions, InfraRegion{Name: name, Targets: list})
			}
			ic.Accounts = append(ic.Accounts, ia)
		}
		v.Clouds = append(v.Clouds, ic)
	}
	// Clouds first, the machines outside one last.
	sort.SliceStable(v.Clouds, func(i, j int) bool { return v.Clouds[j].Name == cloudNames[""] && v.Clouds[i].Name != cloudNames[""] })
	return render(w, r, InfraPage(v))
}

// healthRank orders tiles: what needs attention first.
var healthRank = map[string]int{"bad": 0, "warn": 1, "unmapped": 2, "ok": 3, "empty": 4}

func sortedMapKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
