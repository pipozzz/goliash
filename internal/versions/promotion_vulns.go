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
	active, err := st.ListActiveInstances(ctx, sc)
	if err != nil {
		return err
	}
	sboms, err := st.ListImageSBOMs(ctx, sc)
	if err != nil {
		return err
	}
	// Whether a service's main image of a version in an environment has a readable SBOM.
	type side struct{ service, env, version string }
	read := map[side]bool{}
	for _, in := range active {
		if !in.IsMain || in.Digest == "" {
			continue
		}
		if len(sboms[ParseImage(in.Image).Repo()+"@"+in.Digest].Purls) > 0 {
			read[side{in.ServiceID, in.EnvironmentID, in.Tag}] = true
		}
	}
	// The vulnerabilities of each side.
	vulns := map[side]map[string]VulnRef{}
	for _, f := range rep.Findings {
		ref := VulnRef{ID: f.Key, Severity: f.Severity, Exploited: f.Exploited != nil}
		for _, a := range f.Affected {
			k := side{a.ServiceID, a.EnvironmentID, ParseImage(a.Image).Tag}
			if vulns[k] == nil {
				vulns[k] = map[string]VulnRef{}
			}
			vulns[k][f.Key] = ref
		}
	}
	for i := range list {
		p := &list[i]
		from := side{p.Service.ID, p.From.ID, p.Version}
		to := side{p.Service.ID, p.To.ID, p.Running}
		d := VulnDelta{Known: read[from] && read[to]}
		if d.Known {
			for id, v := range vulns[to] {
				if _, still := vulns[from][id]; !still {
					d.Fixed = append(d.Fixed, v)
				}
			}
			for id, v := range vulns[from] {
				if _, had := vulns[to][id]; !had {
					d.New = append(d.New, v)
				}
			}
			sortVulnRefs(d.Fixed)
			sortVulnRefs(d.New)
		}
		p.Vulns = d
	}
	return nil
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
