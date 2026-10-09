// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package versions

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/pipozzz/goliash/internal/registry"
	"github.com/pipozzz/goliash/internal/store"
)

// SBOMReader reads the package URLs of an image's SBOM attestation. registry.Client implements it.
type SBOMReader interface {
	ImageSBOM(ctx context.Context, repository, digest string, creds registry.Credentials) ([]string, error)
}

const (
	sbomsPerRun    = 5              // SBOMs read per workspace and run
	sbomRetry      = 24 * time.Hour // after a failed read; a read SBOM never changes, its digest does not
	osvTTL         = 24 * time.Hour // how long OSV's answer for a package is trusted
	osvPerRun      = 5000           // packages asked per workspace and run
	detailsPerRun  = 500            // vulnerability records read per run
	detailsRefresh = 7 * 24 * time.Hour
)

// CheckVulnerabilities reads the SBOMs of running images that have one, asks OSV which known vulnerabilities
// affect their packages, and keeps the records of those vulnerabilities.
func (c *Checker) CheckVulnerabilities(ctx context.Context, sc store.Scope) error {
	reader, ok := c.tags.(SBOMReader)
	if c.osv == nil || !ok {
		return nil
	}
	active, err := c.store.ListActiveInstances(ctx, sc)
	if err != nil {
		return err
	}
	evidence, err := c.store.ListImageEvidence(ctx, sc)
	if err != nil {
		return err
	}
	sboms, err := c.store.ListImageSBOMs(ctx, sc)
	if err != nil {
		return err
	}
	now := c.now()
	read := 0
	keys := map[string]bool{}
	for _, in := range active {
		if in.Digest == "" {
			continue
		}
		repo := ParseImage(in.Image).Repo()
		key := repo + "@" + in.Digest
		if keys[key] {
			continue
		}
		keys[key] = true
		b, have := sboms[key]
		if !evidence[key].SBOM || (have && (b.Error == "" || now.Sub(b.FetchedAt) < sbomRetry)) || read >= sbomsPerRun {
			continue
		}
		read++
		purls, err := reader.ImageSBOM(ctx, repo, in.Digest, registry.Credentials{})
		if ctx.Err() != nil {
			return ctx.Err()
		}
		rec := store.ImageSBOM{Repo: repo, Digest: in.Digest, Purls: purls}
		if err != nil {
			rec.Error = err.Error()
		}
		if err := c.store.SetImageSBOM(ctx, sc, rec); err != nil {
			return err
		}
		sboms[key] = rec
	}

	// OSV, for the packages of running images whose answer is missing or old.
	var purls []string
	seen := map[string]bool{}
	for key := range keys {
		for _, p := range sboms[key].Purls {
			if !seen[p] {
				seen[p] = true
				purls = append(purls, p)
			}
		}
	}
	sort.Strings(purls)
	known, err := c.store.PackageVulnsOf(ctx, purls)
	if err != nil {
		return err
	}
	var stale []string
	for _, p := range purls {
		if k, ok := known[p]; !ok || now.Sub(k.CheckedAt) >= osvTTL {
			stale = append(stale, p)
		}
	}
	if len(stale) > osvPerRun {
		stale = stale[:osvPerRun]
	}
	if len(stale) > 0 {
		answers, err := c.osv.QueryBatch(ctx, stale)
		if err != nil {
			return err
		}
		if err := c.store.SetPackageVulns(ctx, answers); err != nil {
			return err
		}
		for p, ids := range answers {
			known[p] = store.PackageVulns{Purl: p, Vulns: ids, CheckedAt: now}
		}
	}

	// The records of the vulnerabilities found, for their CVE aliases, summary and severity.
	records, err := c.store.ListVulns(ctx)
	if err != nil {
		return err
	}
	fetched := 0
	asked := map[string]bool{}
	for _, p := range purls {
		for _, id := range known[p].Vulns {
			if asked[id] || fetched >= detailsPerRun {
				continue
			}
			asked[id] = true
			if r, ok := records[id]; ok && now.Sub(r.FetchedAt) < detailsRefresh {
				continue
			}
			fetched++
			v, err := c.osv.Vuln(ctx, id)
			if err != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				continue
			}
			if err := c.store.SetVuln(ctx, store.Vuln{ID: v.ID, Aliases: v.Aliases, Summary: v.Summary, Severity: v.Severity}); err != nil {
				return err
			}
		}
	}
	return nil
}

// Affected is one running image a vulnerability is in.
type Affected struct {
	Service, Env, Image, Package string
}

// VulnFinding is one vulnerability (its CVE when it has one) and the running images it is in.
type VulnFinding struct {
	Key      string   // the CVE, else the advisory ID
	IDs      []string // every advisory that names it (DEBIAN-CVE-…, DSA-…, GHSA-…)
	Aliases  []string // every CVE those advisories name
	Summary  string
	Severity string
	Affected []Affected
}

// VulnReport is what the SBOMs and OSV say about the running images.
type VulnReport struct {
	Findings []VulnFinding
	Images   int // running images with a digest
	WithSBOM int // of them, with an SBOM read
	Packages int // distinct packages in those SBOMs
	Unasked  int // packages OSV has not answered for yet
	Pending  int // advisories whose record (and so their CVE) is not read yet: an answer may be incomplete
}

// LoadVulnReport builds the report for the running images, envID limiting it to one environment ("" for all).
func LoadVulnReport(ctx context.Context, st *store.Store, sc store.Scope, envID string) (VulnReport, error) {
	var r VulnReport
	active, err := st.ListActiveInstances(ctx, sc)
	if err != nil {
		return r, err
	}
	sboms, err := st.ListImageSBOMs(ctx, sc)
	if err != nil {
		return r, err
	}
	services, err := st.ListServices(ctx, sc)
	if err != nil {
		return r, err
	}
	envs, err := st.ListEnvironments(ctx, sc)
	if err != nil {
		return r, err
	}
	svcName, envName := map[string]string{}, map[string]string{}
	for _, s := range services {
		svcName[s.ID] = s.Name
	}
	for _, e := range envs {
		envName[e.ID] = e.Name
	}
	type use struct{ service, env, image string }
	byPurl := map[string][]use{}
	images := map[string]bool{}
	for _, in := range active {
		if in.Digest == "" || (envID != "" && in.EnvironmentID != envID) {
			continue
		}
		key := ParseImage(in.Image).Repo() + "@" + in.Digest
		if !images[key] {
			images[key] = true
			r.Images++
			if len(sboms[key].Purls) > 0 {
				r.WithSBOM++
			}
		}
		name := svcName[in.ServiceID]
		if name == "" {
			name = in.WorkloadName
		}
		for _, p := range sboms[key].Purls {
			byPurl[p] = append(byPurl[p], use{name, envName[in.EnvironmentID], in.Image})
		}
	}
	purls := make([]string, 0, len(byPurl))
	for p := range byPurl {
		purls = append(purls, p)
	}
	r.Packages = len(purls)
	pv, err := st.PackageVulnsOf(ctx, purls)
	if err != nil {
		return r, err
	}
	r.Unasked = len(purls) - len(pv)
	records, err := st.ListVulns(ctx)
	if err != nil {
		return r, err
	}
	findings := map[string]*VulnFinding{}
	pending := map[string]bool{}
	for p, k := range pv {
		for _, id := range k.Vulns {
			rec, have := records[id]
			if !have && CVEOf(id) == "" {
				pending[id] = true
			}
			key := CVEOf(id)
			for _, a := range rec.Aliases {
				if key == "" {
					key = CVEOf(a)
				}
			}
			if key == "" {
				key = id
			}
			f := findings[key]
			if f == nil {
				f = &VulnFinding{Key: key}
				findings[key] = f
			}
			if !contains(f.IDs, id) {
				f.IDs = append(f.IDs, id)
			}
			for _, a := range append([]string{id}, rec.Aliases...) {
				if c := CVEOf(a); c != "" && !contains(f.Aliases, c) {
					f.Aliases = append(f.Aliases, c)
				}
			}
			if f.Summary == "" {
				f.Summary = rec.Summary
			}
			if severityRank(rec.Severity) > severityRank(f.Severity) {
				f.Severity = rec.Severity
			}
			for _, u := range byPurl[p] {
				a := Affected{Service: u.service, Env: u.env, Image: u.image, Package: purlName(p)}
				if !containsAffected(f.Affected, a) {
					f.Affected = append(f.Affected, a)
				}
			}
		}
	}
	r.Pending = len(pending)
	for _, f := range findings {
		sort.Strings(f.IDs)
		sort.Strings(f.Aliases)
		r.Findings = append(r.Findings, *f)
	}
	sort.Slice(r.Findings, func(i, j int) bool {
		a, b := r.Findings[i], r.Findings[j]
		if severityRank(a.Severity) != severityRank(b.Severity) {
			return severityRank(a.Severity) > severityRank(b.Severity)
		}
		if len(a.Affected) != len(b.Affected) {
			return len(a.Affected) > len(b.Affected)
		}
		return a.Key > b.Key // newest CVEs first
	})
	return r, nil
}

// purlName is a package URL as people read it: openssl 3.0.11-1~deb12u2.
func purlName(p string) string {
	p = strings.SplitN(p, "?", 2)[0]
	if i := strings.LastIndex(p, "/"); i >= 0 {
		p = p[i+1:]
	}
	return strings.Replace(p, "@", " ", 1)
}

func severityRank(s string) int {
	return map[string]int{"LOW": 1, "MODERATE": 2, "MEDIUM": 2, "HIGH": 3, "CRITICAL": 4}[strings.ToUpper(s)]
}

func containsAffected(list []Affected, a Affected) bool {
	for _, x := range list {
		if x == a {
			return true
		}
	}
	return false
}
