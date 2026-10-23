// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package versions

import (
	"context"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/pipozzz/goliash/internal/registry"
	"github.com/pipozzz/goliash/internal/store"
)

// SBOMReader reads the package URLs of an image's SBOM attestation. registry.Client implements it.
type SBOMReader interface {
	ImageSBOM(ctx context.Context, repository, digest string, creds registry.Credentials) (registry.SBOM, error)
}

// NeedsLicenses reports whether an SBOM was read before licenses were kept, and so is read once more for them.
func NeedsLicenses(b store.ImageSBOM) bool {
	return b.Error == "" && len(b.Purls) > 0 && !b.LicensesRead
}

const (
	sbomsPerRun    = 5              // SBOMs read per workspace and run
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
		if !auth.Reads(repo) || !evidence[key].SBOM || (have && ((b.Error == "" && len(b.Purls) > 0 && !NeedsLicenses(b)) || now.Sub(b.FetchedAt) < sbomRetry)) || read >= sbomsPerRun {
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
			if r, ok := records[id]; ok && r.Fixes != nil && now.Sub(r.FetchedAt) < detailsRefresh {
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
	Findings  []VulnFinding
	Images    int // running images with a digest
	WithSBOM  int // of them, with an SBOM read
	Packages  int // distinct packages in those SBOMs
	Unasked   int // packages OSV has not answered for yet
	Pending   int // advisories whose record (and so their CVE) is not read yet: an answer may be incomplete
	Exploited int // findings CISA lists as exploited in the wild
	Fixable   int // findings with a fixed version for an affected package
	PastDue   int // exploited findings still running after CISA's due date
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
	type use struct {
		service, env, image, serviceID, envID string
		since                                 time.Time
	}
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
			byPurl[p] = append(byPurl[p], use{name, envName[in.EnvironmentID], in.Image, in.ServiceID, in.EnvironmentID, in.FirstSeenAt})
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
			for _, u := range byPurl[p] {
				a := Affected{Service: u.service, Env: u.env, Image: u.image, Package: purlName(p), Fix: fix, ServiceID: u.serviceID, EnvironmentID: u.envID, Since: u.since}
				if i := indexAffected(f.Affected, a); i >= 0 {
					if f.Affected[i].Fix == "" {
						f.Affected[i].Fix = fix
					}
					if !u.since.IsZero() && (f.Affected[i].Since.IsZero() || u.since.Before(f.Affected[i].Since)) {
						f.Affected[i].Since = u.since
					}
				} else {
					f.Affected = append(f.Affected, a)
				}
			}
		}
	}
	r.Pending = len(pending)
	if err := addProductFindings(ctx, st, sc, active, services, envName, envID, findings); err != nil {
		return r, err
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
		for _, a := range f.Affected {
			f.Fixable = f.Fixable || a.Fix != ""
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
		if strings.Contains(strings.ToUpper(a.Package), q) || strings.Contains(strings.ToUpper(a.Service), q) {
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
	resolved := resolvedVersions(ctx, st, sc)
	for _, in := range active {
		if envID != "" && in.EnvironmentID != envID {
			continue
		}
		products, version := productOf(svcs[in.ServiceID], in, resolved)
		for _, product := range products {
			for _, v := range known[product+"@"+version].Vulns {
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
					Service: svcs[in.ServiceID].Name, Env: envName[in.EnvironmentID], Image: in.Image, Package: productName(product) + " " + version,
					Fix: v.Fix, ServiceID: in.ServiceID, EnvironmentID: in.EnvironmentID, Since: in.FirstSeenAt,
				}
				if i := indexAffected(f.Affected, a); i >= 0 {
					if f.Affected[i].Fix == "" {
						f.Affected[i].Fix = v.Fix
					}
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
