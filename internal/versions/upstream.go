// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package versions

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/pipozzz/goliash/internal/store"
)

// A distribution's advisory speaks for its own package: Debian's "no fix yet" for nginx is about Debian's nginx.
// An image often installs the product from upstream instead (nginx.org's nginx 1.27.1-1~bookworm in the official
// image), and upstream may have fixed it long before. NVD knows, by product, the versions a CVE affects: asked for
// such CVEs, it tells whether the version that runs is past upstream's fix, or which version fixes it.

const (
	upstreamTTL    = 7 * 24 * time.Hour // how long NVD's ranges for a CVE are trusted
	upstreamPerRun = 6                  // CVEs asked per workspace and run
)

// packageCPEs are the NVD products of distribution packages by name: the usual names, and the binary packages
// that carry a library.
var packageCPEs = func() map[string][]string {
	m := map[string][]string{
		"libssl3": {"openssl:openssl"}, "libssl1.1": {"openssl:openssl"}, "libcurl4": {"haxx:libcurl", "haxx:curl"},
		"libcurl": {"haxx:libcurl", "haxx:curl"}, "zlib1g": {"zlib:zlib"}, "libsqlite3-0": {"sqlite:sqlite"},
		"sqlite-libs": {"sqlite:sqlite"}, "libxml2": {"xmlsoft:libxml2"}, "nodejs": {"nodejs:node.js"},
	}
	for name, cpes := range componentCPEs {
		if _, ok := m[name]; !ok {
			m[name] = cpes
		}
	}
	return m
}()

// splitPackage splits "nginx 1.27.1-1~bookworm" into the package's name and its upstream version, 1.27.1: the
// epoch and the distribution's revision left out.
func splitPackage(pkg string) (name, version string) {
	name, version, _ = strings.Cut(pkg, " ")
	if _, v, ok := strings.Cut(version, ":"); ok {
		version = v
	}
	if i := strings.IndexAny(version, "-~+"); i > 0 {
		version = version[:i]
	}
	return strings.ToLower(name), version
}

// UpstreamFix is what upstream's version ranges say of a package with no fix in its distribution's advisory.
type UpstreamFix struct {
	Product  string // how people name it: nginx
	Running  string // the upstream version that runs: 1.27.1
	Affected bool   // the version that runs is in an affected range
	Fix      string // the first fixed version, when NVD says
	Last     string // the last affected version, when NVD says that instead
	// Sure: the version that runs is past upstream's fix by NVD's ranges, so the distribution's finding does not
	// apply. A range that ends at a last affected version (not a fixed one) counts only for a later release line
	// (1.27 past "up to 1.25.2"): NVD may have closed it at the newest release of its time, before a fix existed.
	Sure bool
}

// Advice says what it means, in a sentence.
func (u UpstreamFix) Advice(cve string) string {
	p := u.Product
	if u.Affected {
		if u.Fix != "" {
			return p + " fixed " + cve + " upstream in " + u.Fix + ": run " + p + " " + u.Fix + " or newer (upstream's own packages or image, if the distribution has none)."
		}
		return p + " " + u.Last + " is the last version upstream lists as affected: a newer " + p + " fixes " + cve + "."
	}
	past := "past the versions it affects"
	switch {
	case u.Fix != "":
		past = "past " + u.Fix + ", where upstream fixed it"
	case u.Last != "":
		past = "past " + u.Last + ", the last version it affects"
	}
	return "Upstream: this runs " + p + " " + u.Running + ", " + past + ". The distribution's \"no fix\" is likely about its own " + p +
		" package: if that is not what runs, mark it not affected."
}

// Reason says why a package is left out: the version that runs is past upstream's fix.
func (u UpstreamFix) Reason(pkg string) string {
	why := pkg + ": upstream " + u.Product + " " + u.Running + " is past the affected versions"
	switch {
	case u.Fix != "":
		why += " (fixed in " + u.Fix + ")"
	case u.Last != "":
		why += " (up to " + u.Last + ")"
	}
	return why + "; the distribution's advisory with no fix is about its own build"
}

// Short says it in a few words, beside "no fix yet".
func (u UpstreamFix) Short() string {
	switch {
	case u.Affected && u.Fix != "":
		return "upstream " + u.Product + " fixed it in " + u.Fix
	case u.Affected:
		return "upstream " + u.Product + " fixed it after " + u.Last
	case u.Fix != "":
		return "upstream: not affected (fixed in " + u.Fix + ")"
	case u.Last != "":
		return "upstream: not affected (only up to " + u.Last + ")"
	}
	return "upstream: not affected"
}

// upstreamFix reads ranges for a package: false when NVD has no range for its products.
func upstreamFix(ranges []store.UpstreamRange, pkg string) (UpstreamFix, bool) {
	name, version := splitPackage(pkg)
	cpes := packageCPEs[name]
	running, ok := ParseVersion(version)
	if len(cpes) == 0 || !ok || len(running.Parts) < minVersion {
		return UpstreamFix{}, false
	}
	u := UpstreamFix{Product: name, Running: versionNumber(running)}
	if len(cpes) > 0 {
		u.Product = productName(cpes[0])
	}
	known := false
	var below Version // the newest fix the running version is past
	for _, r := range ranges {
		if !contains(cpes, r.Product) {
			continue
		}
		known = true
		if r.Exact != "" {
			if e, ok := ParseVersion(r.Exact); ok && running.Compare(e) == 0 {
				u.Affected = true
			}
			continue
		}
		in := atLeast(running, r.StartIncl, false) && atLeast(running, r.StartExcl, true)
		if end, ok := ParseVersion(r.EndExcl); r.EndExcl != "" && ok {
			if in && running.Compare(end) < 0 {
				u.Affected, u.Fix, u.Last = true, r.EndExcl, ""
			} else if running.Compare(end) >= 0 && !u.Affected && (u.Fix == "" || end.Compare(below) > 0) {
				u.Fix, u.Last, below = r.EndExcl, "", end
			}
			continue
		}
		if last, ok := ParseVersion(r.EndIncl); r.EndIncl != "" && ok {
			if in && running.Compare(last) <= 0 {
				u.Affected, u.Fix, u.Last = true, "", r.EndIncl
			} else if running.Compare(last) > 0 && !u.Affected && u.Fix == "" && (u.Last == "" || last.Compare(below) > 0) {
				u.Last, below = r.EndIncl, last
			}
			continue
		}
		if in { // open-ended: every version from its start
			u.Affected, u.Fix, u.Last = true, "", ""
		}
	}
	if !u.Affected {
		switch {
		case u.Fix != "":
			u.Sure = true
		case u.Last != "":
			last, _ := ParseVersion(u.Last)
			u.Sure = laterLine(running, last)
		}
	}
	if !known || (u.Affected && u.Fix == "" && u.Last == "") {
		return UpstreamFix{}, false // nothing to say: unknown, or no fixed version upstream either
	}
	return u, true
}

// laterLine reports whether v is on a later release line (major.minor) than last.
func laterLine(v, last Version) bool {
	for i := range 2 {
		a, b := 0, 0
		if i < len(v.Parts) {
			a = v.Parts[i]
		}
		if i < len(last.Parts) {
			b = last.Parts[i]
		}
		if a != b {
			return a > b
		}
	}
	return false
}

// Ranges asks NVD for the version ranges a CVE affects, for the products Goliash maps packages to.
func (n *NVD) Ranges(ctx context.Context, cve string) ([]store.UpstreamRange, error) {
	if err := n.wait(ctx); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, n.BaseURL+"?"+url.Values{"cveId": {cve}}.Encode(), nil)
	if err != nil {
		return nil, err
	}
	if n.APIKey != "" {
		req.Header.Set("apiKey", n.APIKey)
	}
	resp, err := n.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("NVD answered %s", resp.Status)
	}
	var page nvdPage
	if err := json.NewDecoder(resp.Body).Decode(&page); err != nil {
		return nil, err
	}
	wanted := map[string]bool{}
	for _, cpes := range packageCPEs {
		for _, c := range cpes {
			wanted[c] = true
		}
	}
	out := []store.UpstreamRange{}
	for _, v := range page.Vulnerabilities {
		for _, cfg := range v.CVE.Configurations {
			for _, nd := range cfg.Nodes {
				for _, m := range nd.CPEMatch {
					parts := strings.Split(m.Criteria, ":")
					if !m.Vulnerable || len(parts) < 6 || parts[2] != "a" || !wanted[parts[3]+":"+parts[4]] {
						continue
					}
					r := store.UpstreamRange{
						Product: parts[3] + ":" + parts[4], StartIncl: m.VersionStartIncluding, StartExcl: m.VersionStartExcluding,
						EndIncl: m.VersionEndIncluding, EndExcl: m.VersionEndExcluding,
					}
					if parts[5] != "*" && parts[5] != "-" {
						r.Exact = parts[5]
					}
					out = append(out, r)
				}
			}
		}
	}
	return out, nil
}

// CheckUpstreamFixes asks NVD about the CVEs a distribution has no fix for, in packages Goliash knows upstream's
// product of: exploited and serious ones first, a few per run (more with an NVD API key).
func (c *Checker) CheckUpstreamFixes(ctx context.Context, sc store.Scope) error {
	if c.nvd == nil {
		return nil
	}
	rep, err := LoadVulnReport(ctx, c.store, sc, "")
	if err != nil {
		return err
	}
	known, err := c.store.ListUpstreamCVEs(ctx)
	if err != nil {
		return err
	}
	// Exploited first, then the most severe; and what was left out by upstream's ranges, to keep them fresh.
	fs := slices.Clone(rep.Findings)
	sort.SliceStable(fs, func(i, j int) bool {
		if (fs[i].Exploited != nil) != (fs[j].Exploited != nil) {
			return fs[i].Exploited != nil
		}
		return severityRank(fs[i].Severity) > severityRank(fs[j].Severity)
	})
	var cves []string
	for _, f := range fs {
		for _, a := range f.Affected {
			if name, _ := splitPackage(a.Package); a.Fix == "" && len(packageCPEs[name]) > 0 {
				cves = append(cves, upstreamCVE(f))
				break
			}
		}
	}
	for _, n := range rep.NotAffected {
		if name, _ := splitPackage(n.Package); n.Auto != "" && len(packageCPEs[name]) > 0 {
			cves = append(cves, n.Key)
		}
	}
	limit := upstreamPerRun
	if c.nvd.APIKey != "" {
		limit *= 5
	}
	now, asked := c.now(), 0
	for _, cve := range cves {
		if asked >= limit {
			break
		}
		if k, ok := known[cve]; cve == "" || (ok && now.Sub(k.CheckedAt) < upstreamTTL) {
			continue
		}
		asked++
		known[cve] = store.UpstreamCVE{CVE: cve, CheckedAt: now} // asked once per run
		ranges, err := c.nvd.Ranges(ctx, cve)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			c.log.WarnContext(ctx, "NVD CVE lookup failed", "cve", cve, "err", err)
			continue
		}
		if err := c.store.SetUpstreamCVE(ctx, cve, ranges); err != nil {
			return err
		}
	}
	return nil
}

// upstreamCVE is the CVE a finding is known by at NVD, "" without one.
func upstreamCVE(f VulnFinding) string {
	for _, id := range append([]string{f.Key}, f.Aliases...) {
		if strings.HasPrefix(id, "CVE-") {
			return id
		}
	}
	return ""
}

// addUpstreamFixes notes, on affected packages with no fix in their distribution, what upstream's ranges say. A
// fix upstream for the version that runs makes the finding fixable.
func addUpstreamFixes(known map[string]store.UpstreamCVE, findings map[string]*VulnFinding) {
	for _, f := range findings {
		u, ok := known[upstreamCVE(*f)]
		if !ok {
			continue
		}
		for i := range f.Affected {
			a := &f.Affected[i]
			if a.Fix != "" {
				continue
			}
			if fix, ok := upstreamFix(u.Ranges, a.Package); ok {
				a.Upstream = &fix
			}
		}
	}
}
