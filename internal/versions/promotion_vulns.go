// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package versions

import (
	"context"
	"sort"

	"github.com/pipozzz/goliash/internal/store"
)

// VulnRef is a vulnerability as a promotion changes it.
type VulnRef struct {
	ID        string // the CVE, else the advisory ID
	Severity  string
	Exploited bool // CISA lists it as exploited in the wild
}

// VulnDelta is what promoting a version does to the known vulnerabilities of the environment behind: those it
// fixes (in the running version, not in the promoted one) and those it brings.
type VulnDelta struct {
	Known bool // both versions' images have an SBOM Goliash read
	Fixed []VulnRef
	New   []VulnRef
}

// vulnSide is a service's main image of a version, in one environment or (env "") wherever it runs.
type vulnSide struct{ service, env, version string }

// vulnIndex knows, per service, environment and version, whether its main image has an SBOM Goliash read and
// which known vulnerabilities are in it.
type vulnIndex struct {
	read  map[vulnSide]bool
	vulns map[vulnSide]map[string]VulnRef
}

func loadVulnIndex(ctx context.Context, st *store.Store, sc store.Scope, rep VulnReport) (vulnIndex, error) {
	idx := vulnIndex{read: map[vulnSide]bool{}, vulns: map[vulnSide]map[string]VulnRef{}}
	active, err := st.ListActiveInstances(ctx, sc)
	if err != nil {
		return idx, err
	}
	sboms, err := st.ListImageSBOMs(ctx, sc)
	if err != nil {
		return idx, err
	}
	for _, in := range active {
		if !in.IsMain || in.Digest == "" {
			continue
		}
		if len(sboms[ParseImage(in.Image).Repo()+"@"+in.Digest].Purls) > 0 {
			idx.read[vulnSide{in.ServiceID, in.EnvironmentID, in.Tag}] = true
			idx.read[vulnSide{in.ServiceID, "", in.Tag}] = true
		}
	}
	for _, f := range rep.Findings {
		ref := VulnRef{ID: f.Key, Severity: f.Severity, Exploited: f.Exploited != nil}
		for _, a := range f.Affected {
			tag := ParseImage(a.Image).Tag
			for _, k := range []vulnSide{{a.ServiceID, a.EnvironmentID, tag}, {a.ServiceID, "", tag}} {
				if idx.vulns[k] == nil {
					idx.vulns[k] = map[string]VulnRef{}
				}
				idx.vulns[k][f.Key] = ref
			}
		}
	}
	return idx, nil
}

// delta is what moving from the running side to the new one does to known vulnerabilities.
func (idx vulnIndex) delta(newer, running vulnSide) VulnDelta {
	d := VulnDelta{Known: idx.read[newer] && idx.read[running]}
	if !d.Known {
		return d
	}
	for id, v := range idx.vulns[running] {
		if _, still := idx.vulns[newer][id]; !still {
			d.Fixed = append(d.Fixed, v)
		}
	}
	for id, v := range idx.vulns[newer] {
		if _, had := idx.vulns[running][id]; !had {
			d.New = append(d.New, v)
		}
	}
	sortVulnRefs(d.Fixed)
	sortVulnRefs(d.New)
	return d
}

// AddVulnDeltas fills in each promotion's vulnerability delta, comparing the main images of the version waiting
// and the version running, as their SBOMs and OSV tell.
func AddVulnDeltas(ctx context.Context, st *store.Store, sc store.Scope, list []Promotion) error {
	if len(list) == 0 {
		return nil
	}
	rep, err := LoadVulnReport(ctx, st, sc, "")
	if err != nil {
		return err
	}
	idx, err := loadVulnIndex(ctx, st, sc, rep)
	if err != nil {
		return err
	}
	for i := range list {
		p := &list[i]
		p.Vulns = idx.delta(vulnSide{p.Service.ID, p.From.ID, p.Version}, vulnSide{p.Service.ID, p.To.ID, p.Running})
	}
	return nil
}

// UpgradeVulns is what moving a service in one environment from a version to another does to known
// vulnerabilities: the newer version as it runs anywhere, the running one in that environment.
func UpgradeVulns(ctx context.Context, st *store.Store, sc store.Scope, serviceID, envID, from, to string) (VulnDelta, error) {
	rep, err := LoadVulnReport(ctx, st, sc, "")
	if err != nil {
		return VulnDelta{}, err
	}
	idx, err := loadVulnIndex(ctx, st, sc, rep)
	if err != nil {
		return VulnDelta{}, err
	}
	return idx.delta(vulnSide{serviceID, "", to}, vulnSide{serviceID, envID, from}), nil
}

// sortVulnRefs puts exploited vulnerabilities first, then the most severe, then by ID.
func sortVulnRefs(list []VulnRef) {
	sort.Slice(list, func(i, j int) bool {
		a, b := list[i], list[j]
		if a.Exploited != b.Exploited {
			return a.Exploited
		}
		if severityRank(a.Severity) != severityRank(b.Severity) {
			return severityRank(a.Severity) > severityRank(b.Severity)
		}
		return a.ID < b.ID
	})
}

// Exploited counts the vulnerabilities CISA lists as exploited in the wild.
func Exploited(list []VulnRef) int {
	n := 0
	for _, v := range list {
		if v.Exploited {
			n++
		}
	}
	return n
}
