// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package versions

import (
	"context"
	"fmt"
	"net/url"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/pipozzz/goliash/internal/registry"
	"github.com/pipozzz/goliash/internal/store"
)

// SBOMReader reads the package URLs of an image's SBOM attestation. registry.Client implements it.
type SBOMReader interface {
	ImageSBOM(ctx context.Context, repository, digest string, creds registry.Credentials) (registry.SBOM, error)
}

// VEXReader reads the OpenVEX statements attached to an image. registry.Client implements it.
type VEXReader interface {
	ImageVEX(ctx context.Context, repository, digest string, creds registry.Credentials) ([]registry.VEXStatement, error)
}

const (
	vexPerRun  = 20             // images whose VEX documents are read per run
	vexRefresh = 24 * time.Hour // publishers add statements as they look into new vulnerabilities
)

// hasVEX reports whether a registry lookup found a VEX document for the image.
func hasVEX(ev store.ImageEvidence) bool {
	for _, f := range ev.Found {
		if strings.HasSuffix(f, " vex") || f == "vex artifact" {
			return true
		}
	}
	return false
}

// readVEX reads the VEX documents of running images that have one, a few per run.
func (c *Checker) readVEX(ctx context.Context, sc store.Scope, keys map[string]bool, evidence map[string]store.ImageEvidence, auth RegistryAuth) error {
	reader, ok := c.tags.(VEXReader)
	if !ok {
		return nil
	}
	known, err := c.store.ListImageVEX(ctx, sc)
	if err != nil {
		return err
	}
	now, read := c.now(), 0
	for key := range keys {
		repo, digest, _ := strings.Cut(key, "@")
		if v, ok := known[key]; !hasVEX(evidence[key]) || !auth.Reads(repo) || (ok && now.Sub(v.FetchedAt) < vexRefresh) || read >= vexPerRun {
			continue
		}
		read++
		creds, _ := auth.For(repo)
		list, err := reader.ImageVEX(ctx, repo, digest, creds)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		rec := store.ImageVEX{Repo: repo, Digest: digest, Statements: VEXStatements(list)}
		if err != nil {
			rec.Error = err.Error()
		}
		if err := c.store.SetImageVEX(ctx, sc, rec); err != nil {
			return err
		}
	}
	return nil
}

// VEXStatements converts a registry's statements for the store.
func VEXStatements(list []registry.VEXStatement) []store.VEXStatement {
	out := make([]store.VEXStatement, 0, len(list))
	for _, s := range list {
		out = append(out, store.VEXStatement(s))
	}
	return out
}

// PackageReader reads the operating system packages installed in an image, for an image without an SBOM.
// registry.Client implements it.
type PackageReader interface {
	ImagePackages(ctx context.Context, repository, digest string, creds registry.Credentials) (registry.SBOM, error)
}

// NeedsLicenses reports whether an SBOM was read before licenses were kept, and so is read once more for them.
func NeedsLicenses(b store.ImageSBOM) bool {
	return b.Error == "" && len(b.Purls) > 0 && !b.LicensesRead
}

// appPackagesFrom is when reading an image without an SBOM started to include the application's packages, not
// only the operating system's.
var appPackagesFrom = time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)

// NeedsAppPackages reports whether an image's packages were read from its package database before the application's
// packages were read too (or none were found then): it is read again, once.
func NeedsAppPackages(b store.ImageSBOM) bool {
	return b.Source == "packages" && !b.FetchedAt.IsZero() && b.FetchedAt.Before(appPackagesFrom)
}

const (
	sbomsPerRun    = 5              // SBOMs read per workspace and run
	packagesPerRun = 2              // images without an SBOM read for their packages per workspace and run (layers are big)
	sbomRetry      = 24 * time.Hour // after a failed or empty read; a read SBOM never changes, its digest does not
	osvTTL         = 24 * time.Hour // how long OSV's answer for a package is trusted
	osvPerRun      = 5000           // packages asked per workspace and run
	detailsPerRun  = 500            // vulnerability records read per run
	detailsRefresh = 7 * 24 * time.Hour
)

// CheckVulnerabilities reads the SBOMs of running images that have one, asks OSV which known vulnerabilities
// affect their packages, and keeps the records of those vulnerabilities.
func (c *Checker) CheckVulnerabilities(ctx context.Context, sc store.Scope) error {
	if err := c.refreshKEV(ctx); err != nil && ctx.Err() == nil {
		c.log.WarnContext(ctx, "known exploited vulnerabilities not refreshed", "err", err)
	}
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
	auth := c.registryAuth(ctx, sc)
	read, unpacked := 0, 0
	packages, _ := c.tags.(PackageReader)
	keys := map[string]bool{}
	for _, in := range active {
		if in.CodeDigest != "" { // a .zip Lambda function: its agent sends the packages of its code
			keys[in.PackagesKey("")] = true
			continue
		}
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
		ev, checked := evidence[key]
		if auth.Reads(repo) && checked && !ev.CheckedAt.IsZero() && ev.Error == "" && !ev.SBOM && packages != nil && unpacked < packagesPerRun &&
			(!have || (b.Source == "packages" && b.Error != "" && now.Sub(b.FetchedAt) >= sbomRetry) || NeedsAppPackages(b)) {
			// No SBOM: read the image's own package database and application manifests, so it is not a blind spot.
			unpacked++
			creds, _ := auth.For(repo)
			list, err := packages.ImagePackages(ctx, repo, in.Digest, creds)
			if ctx.Err() != nil {
				return ctx.Err()
			}
			rec := store.ImageSBOM{Repo: repo, Digest: in.Digest, Purls: list.Purls, Source: "packages"}
			if err != nil {
				rec.Error = err.Error()
			}
			if err := c.store.SetImageSBOM(ctx, sc, rec); err != nil {
				return err
			}
			sboms[key] = rec
			continue
		}
		if !auth.Reads(repo) || !ev.SBOM || (have && b.Source != "packages" && ((b.Error == "" && len(b.Purls) > 0 && !NeedsLicenses(b)) || now.Sub(b.FetchedAt) < sbomRetry)) || read >= sbomsPerRun {
			continue
		}
		read++
		creds, _ := auth.For(repo)
		sbom, err := reader.ImageSBOM(ctx, repo, in.Digest, creds)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		rec := store.ImageSBOM{Repo: repo, Digest: in.Digest, Purls: sbom.Purls, Licenses: sbom.Licenses}
		if err != nil {
			rec.Error = err.Error()
		}
		if err := c.store.SetImageSBOM(ctx, sc, rec); err != nil {
			return err
		}
		sboms[key] = rec
	}

	if err := c.readVEX(ctx, sc, keys, evidence, auth); err != nil {
		return err
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
			// Kept until it is a week old, or OSV says it changed since (a distribution added a fix).
			if r, ok := records[id]; ok && r.Fixes != nil && now.Sub(r.FetchedAt) < detailsRefresh && !c.osv.Modified(id).After(r.FetchedAt) {
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
			if err := c.store.SetVuln(ctx, store.Vuln{ID: v.ID, Aliases: v.Aliases, Summary: v.Summary, Severity: v.Severity, Fixes: v.Fixes}); err != nil {
				return err
			}
		}
	}
	return nil
}

// Affected is one running image a vulnerability is in.
type Affected struct {
	Service, Env, Image, Package string
	Fix                          string // the version of the package that fixes it, when known
	ServiceID, EnvironmentID     string
	Since                        time.Time // since when the image has run there (its earliest target)
	Exposed                      []string  // how it can be reached from outside its cluster or host, if it can
	// Mitigation is what was done there instead of an upgrade, for an exploited vulnerability with no fix.
	Mitigation *store.Mitigation
	// Upstream is what NVD says of the product upstream, when the distribution's advisory has no fix.
	Upstream *UpstreamFix
	// VEX is what the image's publisher says of it, when its VEX document has a statement.
	VEX *store.VEXStatement
	// Newer is a newer release of the image's distribution that fixes it, when its own has no fix; Current names
	// the image's own release (Debian 12 (bookworm)).
	Newer   *NewerRelease
	Current string
}

// VulnFinding is one vulnerability (its CVE when it has one) and the running images it is in.
type VulnFinding struct {
	Key      string   // the CVE, else the advisory ID
	IDs      []string // every advisory that names it (DEBIAN-CVE-…, DSA-…, GHSA-…)
	Aliases  []string // every CVE those advisories name
	Summary  string
	Severity string
	Affected []Affected
	// Exploited is set when CISA lists the CVE as exploited in the wild.
	Exploited *store.Exploited
	Fixable   bool      // a fixed version exists for at least one affected package
	Since     time.Time // since when it has run: the earliest of its affected images
}

// NoFix reports whether the vulnerability is exploited in the wild and no affected package has a fixed version:
// something other than an upgrade has to keep it from being used, a mitigation.
func (f VulnFinding) NoFix() bool { return f.Exploited != nil && !f.Fixable }

// Unmitigated are the places where a vulnerability with no fix runs without a mitigation.
func (f VulnFinding) Unmitigated() []Affected {
	if !f.NoFix() {
		return nil
	}
	var out []Affected
	seen := map[string]bool{}
	for _, a := range f.Affected {
		k := a.ServiceID + "|" + a.EnvironmentID
		if a.Mitigation == nil && !seen[k] {
			seen[k] = true
			out = append(out, a)
		}
	}
	return out
}

// MitigateBy is the date a mitigation is due for a vulnerability with no fix: the remediation deadline for an
// exploited vulnerability, from when CISA listed it while it ran (or it started running); zero without a deadline.
func (f VulnFinding) MitigateBy(days int) time.Time {
	if !f.NoFix() || days <= 0 {
		return time.Time{}
	}
	from := f.Since
	if added, err := time.Parse(time.DateOnly, f.Exploited.DateAdded); err == nil && added.After(from) {
		from = added
	}
	if from.IsZero() {
		return time.Time{}
	}
	return from.AddDate(0, 0, days)
}

// IsExposed reports whether an image with the vulnerability can be reached from outside its cluster or host.
func (f VulnFinding) IsExposed() bool {
	for _, a := range f.Affected {
		if len(a.Exposed) > 0 {
			return true
		}
	}
	return false
}

// DueDate is the date CISA set for fixing an exploited vulnerability (BOD 22-01); zero when none is set.
func (f VulnFinding) DueDate() time.Time {
	if f.Exploited == nil || f.Exploited.DueDate == "" {
		return time.Time{}
	}
	d, err := time.Parse("2006-01-02", f.Exploited.DueDate)
	if err != nil {
		return time.Time{}
	}
	return d
}

// PastDue tells whether CISA's due date for an exploited vulnerability has passed while it still runs.
func (f VulnFinding) PastDue(now time.Time) bool {
	d := f.DueDate()
	return !d.IsZero() && now.After(d.AddDate(0, 0, 1))
}

// DaysRunning is how many whole days the vulnerability has run in the report's scope.
func (f VulnFinding) DaysRunning(now time.Time) int {
	if f.Since.IsZero() {
		return 0
	}
	return int(now.Sub(f.Since).Hours() / 24)
}

// VulnReport is what the SBOMs and OSV say about the running images.
type VulnReport struct {
	Findings []VulnFinding
	Images   int // running images with a digest
	WithSBOM int // of them, with an SBOM read (or their package database, FromPackages)
	// FromPackages counts the images without an SBOM whose system packages Goliash read from the image itself.
	FromPackages int
	// Blind are the running images whose packages are not known at all: their vulnerabilities are not either.
	Blind     []BlindImage
	Packages  int // distinct packages in those SBOMs
	Unasked   int // packages OSV has not answered for yet
	Pending   int // advisories whose record (and so their CVE) is not read yet: an answer may be incomplete
	Exploited int // findings CISA lists as exploited in the wild
	Fixable   int // findings with a fixed version for an affected package
	PastDue   int // exploited findings still running after CISA's due date
	// NotAffected are the vulnerabilities left out of Findings for a service: people said they do not affect it,
	// or upstream's version ranges show the version that runs is past the fix.
	NotAffected []NotAffected
}

// NotAffected is a vulnerability left out for a service, and why: an exception people recorded, or Goliash's Auto.
type NotAffected struct {
	Key, Service, ServiceID, Package string
	Exception                        store.VulnException // zero when Goliash left it out itself
	Auto                             string              // why Goliash left it out: upstream fixed it, or a kernel package
}

// BlindImage is a running image whose packages Goliash does not know, and why.
type BlindImage struct {
	Image, Service, Environment string
	Source                      string // the service's source repository, for how to add an SBOM to its build
	Why                         string
}

// LoadVulnReport builds the report for the running images, envID limiting it to one environment ("" for all).
func LoadVulnReport(ctx context.Context, st *store.Store, sc store.Scope, envID string) (VulnReport, error) {
	return memoized(ctx, "vulns "+sc.WorkspaceID+" "+envID, func() (VulnReport, error) { return sharedVulnReport(ctx, st, sc, envID) })
}

// vulnTables are the tables the vulnerability report is read from: a kept report is stale once one is written.
var vulnTables = []string{
	"instances", "services", "environments", "image_sboms", "osv_packages", "osv_vulns", "known_exploited", "mitigations",
	"vuln_exceptions", "upstream_cves", "product_releases", "image_vex", "product_vulns", "image_evidence", "tag_resolutions",
}

// vulnTTL bounds how long a report is reused, for other processes' writes.
const vulnTTL = time.Minute

var vulnCache struct {
	sync.Mutex
	m map[string]keptVulns
}

type keptVulns struct {
	gens string
	at   time.Time
	r    VulnReport
}

// sharedVulnReport is the report reused across requests while its tables are unchanged: every page marks
// exploited vulnerabilities, and building it goes through every package of every image. It is shared: callers
// must not change it in place.
func sharedVulnReport(ctx context.Context, st *store.Store, sc store.Scope, envID string) (VulnReport, error) {
	key := fmt.Sprintf("%p|%s|%s", st, sc.WorkspaceID, envID)
	gens := st.Generations(vulnTables...)
	vulnCache.Lock()
	k, ok := vulnCache.m[key]
	vulnCache.Unlock()
	if ok && k.gens == gens && time.Since(k.at) < vulnTTL {
		return k.r, nil
	}
	r, err := loadVulnReport(ctx, st, sc, envID)
	if err != nil {
		return r, err
	}
	if st.Generations(vulnTables...) == gens {
		vulnCache.Lock()
		if vulnCache.m == nil {
			vulnCache.m = map[string]keptVulns{}
		}
		vulnCache.m[key] = keptVulns{gens: gens, at: time.Now(), r: r}
		vulnCache.Unlock()
	}
	return r, nil
}

func loadVulnReport(ctx context.Context, st *store.Store, sc store.Scope, envID string) (VulnReport, error) {
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
	svcName, envName, svcSource := map[string]string{}, map[string]string{}, map[string]string{}
	for _, s := range services {
		svcName[s.ID], svcSource[s.ID] = s.Name, s.SourceURL
	}
	for _, e := range envs {
		envName[e.ID] = e.Name
	}
	vex, err := st.ListImageVEX(ctx, sc)
	if err != nil {
		return r, err
	}
	type use struct {
		service, env, image, serviceID, envID, key string
		since                                      time.Time
		exposed                                    []string
	}
	byPurl := map[string][]use{}
	images := map[string]bool{}
	for _, in := range active {
		key := in.PackagesKey(ParseImage(in.Image).Repo())
		if key == "" || (envID != "" && in.EnvironmentID != envID) {
			continue
		}
		if !images[key] {
			images[key] = true
			r.Images++
			b := sboms[key]
			switch {
			case len(b.Purls) > 0:
				r.WithSBOM++
				if b.Source == "packages" {
					r.FromPackages++
				}
			default:
				name := svcName[in.ServiceID]
				if name == "" {
					name = in.WorkloadName
				}
				why := blindWhy(b)
				if in.CodeDigest != "" {
					why = codeBlindWhy(b)
				}
				r.Blind = append(r.Blind, BlindImage{Image: in.Image, Service: name, Environment: envName[in.EnvironmentID], Source: svcSource[in.ServiceID], Why: why})
			}
		}
		name := svcName[in.ServiceID]
		if name == "" {
			name = in.WorkloadName
		}
		for _, p := range sboms[key].Purls {
			byPurl[p] = append(byPurl[p], use{name, envName[in.EnvironmentID], in.Image, in.ServiceID, in.EnvironmentID, key, in.FirstSeenAt, in.Exposed})
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
	kev, _, err := st.KnownExploited(ctx)
	if err != nil {
		return r, err
	}
	findings := map[string]*VulnFinding{}
	where := affectedIndex{}
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
			fix := chooseFix(purlVersion(p), fixesFor(p, rec.Fixes))
			var newer *NewerRelease // once a package and advisory, not once an image that has them
			current := ""
			if fix == "" {
				if n, cur, ok := newerReleaseFix(p, rec.Fixes); ok {
					newer, current = &n, cur
				}
			}
			ids := append([]string{id}, f.Aliases...)
			for _, u := range byPurl[p] {
				a := Affected{Service: u.service, Env: u.env, Image: u.image, Package: purlName(p), Fix: fix, ServiceID: u.serviceID, EnvironmentID: u.envID, Since: u.since, Exposed: u.exposed}
				a.VEX = vexFor(vex[u.key].Statements, ids, p)
				a.Newer, a.Current = newer, current
				if i := where.index(f, a); i >= 0 {
					if f.Affected[i].Fix == "" {
						f.Affected[i].Fix = fix
					}
					if f.Affected[i].VEX == nil {
						f.Affected[i].VEX = a.VEX
					}
					if f.Affected[i].Newer == nil && f.Affected[i].Fix == "" {
						f.Affected[i].Newer, f.Affected[i].Current = a.Newer, a.Current
					}
					f.Affected[i].Exposed = union(f.Affected[i].Exposed, u.exposed)
					if !u.since.IsZero() && (f.Affected[i].Since.IsZero() || u.since.Before(f.Affected[i].Since)) {
						f.Affected[i].Since = u.since
					}
				} else {
					where.add(f, a)
				}
			}
		}
	}
	r.Pending = len(pending)
	if err := addProductFindings(ctx, st, sc, active, services, envName, envID, findings); err != nil {
		return r, err
	}
	mitigations, err := st.ListOpenMitigations(ctx, sc)
	if err != nil {
		return r, err
	}
	exceptions, err := st.ListOpenVulnExceptions(ctx, sc)
	if err != nil {
		return r, err
	}
	if upstream, err := st.ListUpstreamCVEs(ctx); err == nil {
		releases, _ := st.ListProductReleases(ctx)
		addUpstreamFixes(upstream, releases, findings)
	}
	// What people said does not affect a service goes, what upstream fixed before the version that runs, and the
	// kernel's packages: from the finding, and the finding when nothing is left.
	listed := map[string]bool{} // not affected entries already listed: a set, not a search through the list
	for key, f := range findings {
		kept := f.Affected[:0]
		for _, a := range f.Affected {
			if why := autoNotAffected(a); why != "" {
				if k := f.Key + "|" + a.ServiceID + "|" + a.Package; !listed[k] {
					listed[k] = true
					r.NotAffected = append(r.NotAffected, NotAffected{Key: f.Key, Service: a.Service, ServiceID: a.ServiceID, Package: a.Package, Auto: why})
				}
				continue
			}
			i := slices.IndexFunc(exceptions, func(e store.VulnException) bool {
				return e.ServiceID == a.ServiceID && (e.CVE == f.Key || slices.Contains(f.Aliases, e.CVE))
			})
			if i < 0 {
				kept = append(kept, a)
				continue
			}
			if k := f.Key + "|exception|" + exceptions[i].ID; !listed[k] {
				listed[k] = true
				r.NotAffected = append(r.NotAffected, NotAffected{Key: f.Key, Service: a.Service, ServiceID: a.ServiceID, Package: a.Package, Exception: exceptions[i]})
			}
		}
		f.Affected = kept
		if len(f.Affected) == 0 {
			delete(findings, key)
		}
	}
	for _, f := range findings {
		sort.Strings(f.IDs)
		sort.Strings(f.Aliases)
		for _, c := range append([]string{f.Key}, f.Aliases...) {
			if e, ok := kev[c]; ok {
				f.Exploited = &e
				break
			}
		}
		for i := range f.Affected {
			a := &f.Affected[i]
			for j := range mitigations {
				m := &mitigations[j]
				if m.ServiceID == a.ServiceID && m.EnvironmentID == a.EnvironmentID && (m.CVE == f.Key || slices.Contains(f.Aliases, m.CVE)) {
					a.Mitigation = m
				}
			}
			f.Fixable = f.Fixable || a.Fix != "" || a.Newer != nil || (a.Upstream != nil && a.Upstream.Affected)
			if !a.Since.IsZero() && (f.Since.IsZero() || a.Since.Before(f.Since)) {
				f.Since = a.Since
			}
		}
		if f.Exploited != nil {
			r.Exploited++
		}
		if f.PastDue(time.Now()) {
			r.PastDue++
		}
		if f.Fixable {
			r.Fixable++
		}
		r.Findings = append(r.Findings, *f)
	}
	sort.Slice(r.Findings, func(i, j int) bool {
		a, b := r.Findings[i], r.Findings[j]
		if (a.Exploited != nil) != (b.Exploited != nil) {
			return a.Exploited != nil
		}
		if a.IsExposed() != b.IsExposed() { // reachable from outside first
			return a.IsExposed()
		}
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

// affectedIndex finds an affected image in a finding by service, environment, image and package without searching
// its list: a vulnerability in a package every image has is in hundreds of them.
type affectedIndex map[*VulnFinding]map[string]int

func affectedKey(a Affected) string {
	return a.Service + "\x00" + a.Env + "\x00" + a.Image + "\x00" + a.Package
}

func (x affectedIndex) index(f *VulnFinding, a Affected) int {
	if i, ok := x[f][affectedKey(a)]; ok {
		return i
	}
	return -1
}

func (x affectedIndex) add(f *VulnFinding, a Affected) {
	if x[f] == nil {
		x[f] = map[string]int{}
	}
	x[f][affectedKey(a)] = len(f.Affected)
	f.Affected = append(f.Affected, a)
}

// indexAffected finds the same service, environment, image and package in list, fix aside.
func indexAffected(list []Affected, a Affected) int {
	for i, x := range list {
		if x.Service == a.Service && x.Env == a.Env && x.Image == a.Image && x.Package == a.Package {
			return i
		}
	}
	return -1
}

// purlVersion is the version of a package URL.
func purlVersion(p string) string {
	p = strings.SplitN(p, "?", 2)[0]
	_, v, _ := strings.Cut(p[strings.LastIndex(p, "/")+1:], "@")
	if u, err := url.PathUnescape(v); err == nil {
		v = u
	}
	return v
}

// chooseFix picks the version to move to among those that fix an advisory: the lowest above the running
// one (log4j 2.14.1 -> 2.15.0, not the 2.12.2 of an older branch), else the last listed.
func chooseFix(running string, fixes []string) string {
	if len(fixes) == 0 {
		return ""
	}
	cur, ok := ParseVersion(running)
	if !ok {
		return fixes[len(fixes)-1]
	}
	best, found := "", false
	var low Version
	for _, f := range fixes {
		v, ok := ParseVersion(f)
		if !ok || v.Compare(cur) <= 0 {
			continue
		}
		if !found || v.Compare(low) < 0 {
			best, low, found = f, v, true
		}
	}
	if !found {
		return fixes[len(fixes)-1]
	}
	return best
}

// AlertVulnerabilities announces, once per service and environment, the vulnerabilities exploited in the
// wild (CISA KEV) or rated critical that run there: a vulnerability event each, handed to notification rules. The
// workspace's first complete look is recorded without announcing, so an installation does not hear about
// every vulnerability it already runs.
func (c *Checker) AlertVulnerabilities(ctx context.Context, sc store.Scope) error {
	if c.osv == nil {
		return nil
	}
	r, err := LoadVulnReport(ctx, c.store, sc, "")
	if err != nil {
		return err
	}
	done, baseline, err := c.store.VulnAlerts(ctx, sc)
	if err != nil {
		return err
	}
	if !baseline && (r.WithSBOM == 0 || r.Pending > 0 || r.Unasked > 0) {
		return nil // wait for a complete picture before the first look
	}
	var record []store.VulnAlert
	var evs []store.Event
	now := c.now()
	// Past-due reminders start after the first look that knows them: vulnerabilities already past due then (an
	// upgrade from a Goliash without reminders) are recorded, not announced again.
	dueBaseline := store.VulnAlert{CVE: "past-due baseline"}
	remind := done[dueBaseline]
	if !remind {
		record = append(record, dueBaseline)
	}
	for _, f := range r.Findings {
		critical := strings.EqualFold(f.Severity, "CRITICAL")
		if f.Exploited == nil && !critical {
			continue
		}
		pastDue := f.PastDue(now)
		for _, a := range f.Affected {
			if a.ServiceID == "" {
				continue
			}
			key := store.VulnAlert{CVE: f.Key, ServiceID: a.ServiceID, EnvironmentID: a.EnvironmentID}
			// The reminder that CISA's due date passed while it still runs is announced once more, apart.
			due := store.VulnAlert{CVE: f.Key + pastDueSuffix, ServiceID: a.ServiceID, EnvironmentID: a.EnvironmentID}
			announce := !done[key] || (pastDue && !done[due])
			if !announce {
				continue
			}
			fresh := !done[key] // its first alert
			for _, k := range []store.VulnAlert{key, due} {
				if k == key || pastDue {
					if !done[k] {
						done[k] = true
						record = append(record, k)
					}
				}
			}
			if !baseline || (!fresh && !remind) {
				continue
			}
			note := vulnNote(f)
			if pastDue {
				note += " " + PastDueFlag
			}
			evs = append(evs, store.Event{
				Type: "vulnerability", ServiceID: a.ServiceID, EnvironmentID: a.EnvironmentID,
				FromVersion: a.Package, ToVersion: a.Fix, Note: note, Source: "poll", At: now,
			})
		}
	}
	if len(record) == 0 && baseline {
		return nil
	}
	if err := c.store.RecordVulnAlerts(ctx, sc, record, !baseline); err != nil {
		return err
	}
	// Announced, not kept in the history of version events: vulnerability_alerts records what was announced.
	c.emit(sc, evs)
	return nil
}

// PastDueFlag marks a vulnerability alert sent because CISA's due date for fixing it passed while it runs.
const PastDueFlag = "past-due"

// pastDueSuffix keys the past-due reminder apart from the first alert in vulnerability_alerts.
const pastDueSuffix = " " + PastDueFlag

// vulnNote is a vulnerability event's note: the CVE, then exploited, ransomware and severity when they apply.
func vulnNote(f VulnFinding) string {
	parts := []string{f.Key}
	if f.Exploited != nil {
		parts = append(parts, "exploited")
		if f.Exploited.Ransomware {
			parts = append(parts, "ransomware")
		}
	}
	if f.Severity != "" {
		parts = append(parts, strings.ToLower(f.Severity))
	}
	return strings.Join(parts, " ")
}

// FindingMatches reports whether a finding answers a query (upper case): a CVE or advisory ID, a package or
// a service.
func FindingMatches(f VulnFinding, q string) bool {
	for _, id := range append(append([]string{f.Key}, f.IDs...), f.Aliases...) {
		if strings.Contains(strings.ToUpper(id), q) {
			return true
		}
	}
	for _, a := range f.Affected {
		if strings.Contains(strings.ToUpper(a.Package), q) || strings.Contains(strings.ToUpper(a.Service), q) || strings.ToUpper(a.Image) == q {
			return true
		}
	}
	return false
}

// addProductFindings adds the CVEs NVD says affect the product versions running (PostgreSQL 16.2, nginx 1.25.3),
// to the findings by CVE: the same CVE from an SBOM and from NVD is one finding.
func addProductFindings(ctx context.Context, st *store.Store, sc store.Scope, active []store.Instance, services []store.Service, envName map[string]string, envID string, findings map[string]*VulnFinding) error {
	known, err := st.ListProductVulns(ctx)
	if err != nil || len(known) == 0 {
		return err
	}
	svcs := make(map[string]store.Service, len(services))
	for _, s := range services {
		svcs[s.ID] = s
	}
	evidence, err := st.ListImageEvidence(ctx, sc)
	if err != nil {
		return err
	}
	resolved := resolvedVersions(ctx, st, sc)
	for _, in := range active {
		if envID != "" && in.EnvironmentID != envID {
			continue
		}
		uses := productsOf(svcs[in.ServiceID], in, resolved, evidence[ParseImage(in.Image).Repo()+"@"+in.Digest])
		advised := false // the product's own GitHub advisories were read: NVD's answer for it is not used
		for _, use := range uses {
			if _, ok := known[use.CPE+"@"+use.Version]; ok && use.fromGitHub() {
				advised = true
			}
		}
		for _, use := range uses {
			if use.Own && advised && !use.fromGitHub() {
				continue
			}
			for _, v := range known[use.CPE+"@"+use.Version].Vulns {
				f := findings[v.CVE]
				if f == nil {
					f = &VulnFinding{Key: v.CVE}
					findings[v.CVE] = f
				}
				if !contains(f.IDs, v.CVE) {
					f.IDs = append(f.IDs, v.CVE)
				}
				if !contains(f.Aliases, v.CVE) {
					f.Aliases = append(f.Aliases, v.CVE)
				}
				if f.Summary == "" {
					f.Summary = v.Summary
				}
				if severityRank(v.Severity) > severityRank(f.Severity) {
					f.Severity = v.Severity
				}
				a := Affected{
					Service: svcs[in.ServiceID].Name, Env: envName[in.EnvironmentID], Image: in.Image, Package: use.Name + " " + use.Version,
					Fix: v.Fix, ServiceID: in.ServiceID, EnvironmentID: in.EnvironmentID, Since: in.FirstSeenAt, Exposed: in.Exposed,
				}
				if i := indexAffected(f.Affected, a); i >= 0 {
					if f.Affected[i].Fix == "" {
						f.Affected[i].Fix = v.Fix
					}
					f.Affected[i].Exposed = union(f.Affected[i].Exposed, in.Exposed)
				} else {
					f.Affected = append(f.Affected, a)
				}
			}
		}
	}
	return nil
}

// productName is a CPE product as people say it: postgresql, nginx (for f5:nginx_open_source too), node.js.
func productName(cpe string) string {
	_, product, _ := strings.Cut(cpe, ":")
	product = strings.TrimSuffix(strings.TrimSuffix(product, "_open_source"), "_server")
	return strings.ReplaceAll(product, "_", " ")
}

// blindWhy says why an image's packages are unknown.
// codeBlindWhy says why the packages of a .zip Lambda function's code are not known.
func codeBlindWhy(b store.ImageSBOM) string {
	switch {
	case b.FetchedAt.IsZero():
		return "the code of this .zip function is not read yet (its agent reads a few functions per round)"
	case b.Error != "":
		return "its code could not be read: " + b.Error
	}
	return "no packages found in its code (no node_modules, Python packages, jars, Go build information, gems or deps.json)"
}

func blindWhy(b store.ImageSBOM) string {
	switch {
	case b.Source == "packages" && strings.Contains(b.Error, "no dpkg or apk package database"):
		return "no SBOM, no dpkg or apk package database and no application manifests (scratch, distroless, RPM-based or built from source)"
	case b.Error != "":
		return "no SBOM; " + readFailure(b.Error)
	case b.FetchedAt.IsZero():
		return "no SBOM; its packages are not read yet (private registries: give the server read access)"
	}
	return "no SBOM"
}

// readFailure says in a few words why reading an image from its registry failed.
func readFailure(err string) string {
	switch {
	case strings.Contains(err, "MANIFEST_UNKNOWN") || strings.Contains(err, "BLOB_UNKNOWN") || strings.Contains(err, " 404"):
		return "the registry no longer has this image (deleted, or the tag was pushed again)"
	case strings.Contains(err, "denied") || strings.Contains(err, " 401") || strings.Contains(err, " 403"):
		return "the registry refused the server: give it read access under Settings → Registries"
	case strings.Contains(err, "larger than"):
		return "the image is too large to read for its packages"
	case strings.Contains(err, "zstd"):
		return "its layers are zstd-compressed, which Goliash does not read"
	}
	if len(err) > 120 {
		err = err[:120] + "…"
	}
	return "reading it failed: " + err
}

// union is a with what b adds, each once, sorted.
func union(a, b []string) []string {
	if len(b) == 0 {
		return a
	}
	out := append([]string{}, a...)
	for _, s := range b {
		if !slices.Contains(out, s) {
			out = append(out, s)
		}
	}
	slices.Sort(out)
	return out
}
