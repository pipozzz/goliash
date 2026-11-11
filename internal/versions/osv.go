// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package versions

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// OSV asks osv.dev (or a mirror) which known vulnerabilities affect packages. Only package names and
// versions are sent, as package URLs or ecosystem, name and version.
type OSV struct {
	HTTP    *http.Client
	BaseURL string // https://api.osv.dev/v1

	mu       sync.Mutex
	modified map[string]time.Time // when OSV last changed each record, as its batch answers say
}

// Modified is when OSV last changed a record, as a batch query since the server started said; zero when unknown.
func (o *OSV) Modified(id string) time.Time {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.modified[id]
}

// NewOSV returns a client for baseURL; empty means osv.dev.
func NewOSV(baseURL string) *OSV {
	if baseURL == "" {
		baseURL = "https://api.osv.dev/v1"
	}
	return &OSV{HTTP: &http.Client{Timeout: 60 * time.Second}, BaseURL: strings.TrimSuffix(baseURL, "/")}
}

// SetOSV gives the checker an OSV client, so it looks up the vulnerabilities of running images' packages.
func (c *Checker) SetOSV(o *OSV) { c.osv = o }

// OSVEnabled reports whether vulnerabilities are looked up.
func (c *Checker) OSVEnabled() bool { return c != nil && c.osv != nil }

type osvQuery struct {
	Package struct {
		Name      string `json:"name,omitempty"`
		Ecosystem string `json:"ecosystem,omitempty"`
		Purl      string `json:"purl,omitempty"`
	} `json:"package"`
	Version   string `json:"version,omitempty"`
	PageToken string `json:"page_token,omitempty"` // the next page of a package with more answers than one holds
}

// osvQueryFor turns a package URL into an OSV query: Debian, Ubuntu, Alpine, Wolfi and Chainguard packages by
// ecosystem ("Debian:12"), name and version, others (npm, PyPI, Go, Maven …) by their purl without qualifiers.
func osvQueryFor(purl string) (osvQuery, bool) {
	qs := osvQueries(purl)
	if len(qs) == 0 {
		return osvQuery{}, false
	}
	return qs[0], true
}

// osvQueries are the queries to try for a package URL, in order, until one finds something. Wolfi and
// Chainguard advisories are filed under the source package (the origin), some under the package itself. The
// origin without its release line (openssl for openssl-4.0) is not tried: advisories of another release line,
// often open-ended, would match every version of this one.
func osvQueries(purl string) []osvQuery {
	if v, ok := queriesMemo.Load(purl); ok {
		return v.([]osvQuery)
	}
	q := parseQueries(purl)
	if queriesMemoN.Add(1) > memoMax { // a bound, not an eviction policy: start again
		queriesMemo.Clear()
		queriesMemoN.Store(0)
	}
	queriesMemo.Store(purl, q)
	return q
}

// queriesMemo keeps what a package URL is asked as: the vulnerability report asks it for every advisory of every
// package, many thousand times a page.
var (
	queriesMemo  sync.Map
	queriesMemoN atomic.Int64
)

const memoMax = 200000

func parseQueries(purl string) []osvQuery {
	q, ok := osvPrimary(purl)
	if !ok {
		return nil
	}
	out := []osvQuery{q}
	if base, ok := strings.CutSuffix(q.Package.Ecosystem, "::baseos"); ok { // a Red Hat package from AppStream
		alt := q
		alt.Package.Ecosystem = base + "::appstream"
		return append(out, alt)
	}
	if strings.HasPrefix(q.Package.Ecosystem, "Rocky Linux:") { // filed under the package itself, sometimes
		if name, _, _ := strings.Cut(purlName(purl), " "); name != q.Package.Name {
			alt := q
			alt.Package.Name = name
			out = append(out, alt)
		}
		return out
	}
	if q.Package.Ecosystem != "Wolfi" && q.Package.Ecosystem != "Chainguard" {
		return out
	}
	u, _ := url.Parse(purl)
	name := purlName(purl)
	name, _, _ = strings.Cut(name, " ")
	seen := map[string]bool{q.Package.Name: true}
	for _, n := range []string{name, u.Query().Get("origin")} {
		if n != "" && !seen[n] {
			seen[n] = true
			alt := q
			alt.Package.Name = n
			out = append(out, alt)
		}
	}
	return out
}

func osvPrimary(purl string) (osvQuery, bool) {
	var q osvQuery
	u, err := url.Parse(purl)
	if err != nil || u.Scheme != "pkg" {
		return q, false
	}
	path := u.Opaque
	if path == "" {
		path = strings.TrimPrefix(u.Path, "/")
	}
	typ, rest, ok := strings.Cut(path, "/")
	if !ok {
		return q, false
	}
	nameVer := rest
	if i := strings.LastIndex(rest, "/"); i >= 0 {
		nameVer = rest[i+1:]
	}
	name, version, ok := strings.Cut(nameVer, "@")
	if !ok || version == "" {
		return q, false
	}
	if v, err := url.PathUnescape(version); err == nil {
		version = v
	}
	qs := u.Query()
	osVersion := qs.Get("os_version")
	if osVersion == "" {
		osVersion = qs.Get("distro_version")
	}
	switch typ {
	case "deb":
		distro := strings.ToLower(strings.SplitN(rest, "/", 2)[0])
		if osVersion == "" || (distro != "debian" && distro != "ubuntu") {
			return q, false
		}
		if distro == "debian" {
			osVersion = strings.SplitN(osVersion, ".", 2)[0]
		}
		q.Package.Ecosystem = map[string]string{"debian": "Debian", "ubuntu": "Ubuntu"}[distro] + ":" + osVersion
		q.Package.Name, q.Version = name, version
		if src := qs.Get("upstream"); src != "" { // the source package OSV files Debian advisories under
			name, version, _ := strings.Cut(src, "@")
			q.Package.Name = name
			if version != "" { // the source's own version, when a binary's differs (telnet 0.17+2.5 of inetutils 2:2.5)
				q.Version = version
			}
		}
	case "apk":
		if eco := map[string]string{"wolfi": "Wolfi", "chainguard": "Chainguard"}[strings.ToLower(strings.SplitN(rest, "/", 2)[0])]; eco != "" {
			q.Package.Ecosystem, q.Package.Name, q.Version = eco, name, version
			if origin := qs.Get("origin"); origin != "" { // the source package OSV files advisories under
				q.Package.Name = origin
			}
			return q, true
		}
		if osVersion == "" {
			return q, false
		}
		parts := strings.SplitN(osVersion, ".", 3)
		if len(parts) < 2 {
			return q, false
		}
		q.Package.Ecosystem = "Alpine:v" + parts[0] + "." + parts[1]
		q.Package.Name, q.Version = name, version
		for _, k := range []string{"origin", "upstream"} { // Alpine's advisories are filed under the source package
			if src := qs.Get(k); src != "" {
				q.Package.Name = strings.SplitN(src, "@", 2)[0]
				break
			}
		}
	case "rpm":
		return rpmQuery(strings.ToLower(strings.SplitN(rest, "/", 2)[0]), name, version, qs)
	default:
		q.Package.Purl = strings.SplitN(purl, "?", 2)[0]
	}
	return q, true
}

// rpmEcosystems are OSV's ecosystems of the RPM distributions it has advisories for, by the package URL's
// namespace, and whether they are filed under the source package (Rocky: openssl, not openssl-libs). Red Hat's
// are filed by binary package, under the repository each comes from ("::baseos", else "::appstream"). Others
// (Fedora, Amazon Linux, CentOS Stream, Oracle Linux) are not asked: OSV has nothing of theirs, and asking by
// package URL alone would match the advisories of every Red Hat release.
var rpmEcosystems = map[string]struct {
	prefix string
	source bool
}{
	"redhat": {"Red Hat:enterprise_linux:", false}, "rhel": {"Red Hat:enterprise_linux:", false},
	"rocky": {"Rocky Linux:", true}, "almalinux": {"AlmaLinux:", false},
}

// rpmQuery is the query for an RPM package: the version with its epoch (1:3.0.7-27.el9), as RPM compares them.
// The release's major version comes from os_version (Goliash's own reading) or distro (rhel-9.4, as SBOM tools
// write it).
func rpmQuery(namespace, name, version string, qs url.Values) (osvQuery, bool) {
	var q osvQuery
	eco, ok := rpmEcosystems[namespace]
	if !ok {
		return q, false
	}
	release := qs.Get("os_version")
	if release == "" {
		d := qs.Get("distro")
		release = d[strings.LastIndex(d, "-")+1:]
	}
	major, _, _ := strings.Cut(release, ".")
	if major == "" || strings.Trim(major, "0123456789") != "" {
		return q, false
	}
	q.Package.Ecosystem = eco.prefix + major
	if strings.HasPrefix(eco.prefix, "Red Hat") {
		q.Package.Ecosystem += "::baseos"
	}
	q.Package.Name, q.Version = name, version
	if e := qs.Get("epoch"); e != "" && e != "0" {
		q.Version = e + ":" + version
	}
	if src := rpmSource(qs.Get("upstream")); eco.source && src != "" {
		q.Package.Name = src
	}
	return q, true
}

// rpmSource is a source package's name from an upstream qualifier: Goliash writes the name (openssl), SBOM tools
// the file (openssl-3.0.7-27.el9.src.rpm).
func rpmSource(upstream string) string {
	if !strings.HasSuffix(upstream, ".rpm") {
		return upstream
	}
	s := strings.TrimSuffix(strings.TrimSuffix(upstream, ".src.rpm"), ".nosrc.rpm")
	for range 2 {
		i := strings.LastIndexByte(s, '-')
		if i <= 0 {
			return ""
		}
		s = s[:i]
	}
	return s
}

// QueryBatch returns the IDs of the vulnerabilities affecting each package URL; packages OSV cannot be asked
// about are left out.
func (o *OSV) QueryBatch(ctx context.Context, purls []string) (map[string][]string, error) {
	return o.queryAll(ctx, purls, "")
}

// newestVersion is a version newer than any a distribution will publish: dpkg compares the epoch first.
const newestVersion = "999:999999999"

// QueryNewest returns, for Debian and Ubuntu packages, the vulnerabilities that would still affect the newest
// version of each: those a package has and this one has not, a newer version fixes. It costs a query of IDs, not
// the records, which for a Linux kernel weigh gigabytes (thousands of them, each listing every kernel build).
func (o *OSV) QueryNewest(ctx context.Context, purls []string) (map[string][]string, error) {
	var asked []string
	for _, p := range purls {
		if newestAsked(p) {
			asked = append(asked, p)
		}
	}
	if len(asked) == 0 {
		return map[string][]string{}, nil
	}
	return o.queryAll(ctx, asked, newestVersion)
}

// newestAsked reports whether a package's newest version can be asked about: Debian and Ubuntu, whose
// advisories OSV matches by version range.
func newestAsked(p string) bool {
	qs := osvQueries(p)
	return len(qs) > 0 && (strings.HasPrefix(qs[0].Package.Ecosystem, "Debian:") || strings.HasPrefix(qs[0].Package.Ecosystem, "Ubuntu:"))
}

// NewestKey is where the answer for a package's newest version is kept, next to the package's own.
func NewestKey(p string) string { return p + "#newest" }

func (o *OSV) queryAll(ctx context.Context, purls []string, version string) (map[string][]string, error) {
	out := map[string][]string{}
	// Each round asks the next query of every package the earlier ones found nothing for.
	for round := 0; ; round++ {
		var queries []osvQuery
		var asked []string
		for _, p := range purls {
			if ids, done := out[p]; done && len(ids) > 0 {
				continue
			}
			if qs := osvQueries(p); round < len(qs) {
				q := qs[round]
				if version != "" {
					q.Version = version
				}
				queries = append(queries, q)
				asked = append(asked, p)
			}
		}
		if len(queries) == 0 {
			return out, nil
		}
		if err := o.queryBatch(ctx, queries, asked, out); err != nil {
			return nil, err
		}
	}
}

func (o *OSV) queryBatch(ctx context.Context, queries []osvQuery, asked []string, out map[string][]string) error {
	// OSV answers at most a thousand vulnerabilities a query, and a page token for the rest: a Linux kernel has
	// thousands. The next pages are asked until none is left.
	for page := 0; len(queries) > 0 && page < 50; page++ {
		var more []osvQuery
		var moreAsked []string
		for start := 0; start < len(queries); start += 1000 {
			end := min(start+1000, len(queries))
			body, _ := json.Marshal(map[string]any{"queries": queries[start:end]})
			var res struct {
				Results []struct {
					Vulns []struct {
						ID       string    `json:"id"`
						Modified time.Time `json:"modified"`
					} `json:"vulns"`
					NextPageToken string `json:"next_page_token"`
				} `json:"results"`
			}
			if err := o.post(ctx, "/querybatch", body, &res); err != nil {
				return err
			}
			for i, r := range res.Results {
				if start+i >= len(asked) {
					break
				}
				p := asked[start+i]
				ids := out[p]
				if page == 0 || ids == nil {
					ids = []string{}
				}
				for _, v := range r.Vulns {
					ids = append(ids, v.ID)
					o.noteModified(v.ID, v.Modified)
				}
				out[p] = ids
				if r.NextPageToken != "" {
					q := queries[start+i]
					q.PageToken = r.NextPageToken
					more = append(more, q)
					moreAsked = append(moreAsked, p)
				}
			}
		}
		queries, asked = more, moreAsked
	}
	return nil
}

// OSVVuln is what Goliash keeps of a vulnerability.
type OSVVuln struct {
	ID       string
	Aliases  []string
	Summary  string
	Severity string              // CRITICAL, HIGH, MODERATE, LOW when the record says; empty otherwise
	Fixes    map[string][]string // "ecosystem|package" -> versions that fix it
}

// osvRecord is the part of an OSV vulnerability record Goliash reads.
type osvRecord struct {
	ID               string    `json:"id"`
	Modified         time.Time `json:"modified"`
	Aliases          []string  `json:"aliases"`
	Upstream         []string  `json:"upstream"`
	Summary          string    `json:"summary"`
	Details          string    `json:"details"`
	DatabaseSpecific struct {
		Severity string `json:"severity"`
	} `json:"database_specific"`
	Severities []struct {
		Type  string `json:"type"`
		Score string `json:"score"`
	} `json:"severity"`
	Affected []struct {
		Package struct {
			Name      string `json:"name"`
			Ecosystem string `json:"ecosystem"`
		} `json:"package"`
		EcosystemSpecific struct {
			Urgency string `json:"urgency"` // Debian's: unimportant, low, medium, high
			Imports []struct {
				Path string `json:"path"` // a Go package with the vulnerable code: net/http
			} `json:"imports"`
		} `json:"ecosystem_specific"`
		Ranges []struct {
			Events []struct {
				Fixed string `json:"fixed"`
			} `json:"events"`
		} `json:"ranges"`
	} `json:"affected"`
}

// Vuln reads one vulnerability record.
func (o *OSV) Vuln(ctx context.Context, id string) (OSVVuln, error) {
	var rec osvRecord
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, o.BaseURL+"/vulns/"+url.PathEscape(id), nil)
	if err != nil {
		return OSVVuln{}, err
	}
	if err := o.do(req, &rec); err != nil {
		return OSVVuln{}, err
	}
	return rec.vuln(), nil
}

func (o *OSV) noteModified(id string, at time.Time) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.modified == nil {
		o.modified = map[string]time.Time{}
	}
	if at.After(o.modified[id]) {
		o.modified[id] = at
	}
}

// vuln is what Goliash keeps of a record.
func (rec osvRecord) vuln() OSVVuln {
	v := OSVVuln{ID: rec.ID, Aliases: append(rec.Aliases, rec.Upstream...), Summary: rec.Summary, Severity: strings.ToUpper(rec.DatabaseSpecific.Severity), Fixes: map[string][]string{}}
	negligible := false // Ubuntu's priority, for all its releases
	for _, s := range rec.Severities {
		negligible = negligible || s.Type == "Ubuntu" && strings.EqualFold(s.Score, "negligible")
	}
	v.Fixes[fixesFormat] = []string{fixesFormatNow}
	for _, a := range rec.Affected {
		key := a.Package.Ecosystem + "|" + a.Package.Name
		for _, imp := range a.EcosystemSpecific.Imports {
			if imp.Path != "" && !contains(v.Fixes[importsKey+key], imp.Path) {
				v.Fixes[importsKey+key] = append(v.Fixes[importsKey+key], imp.Path)
			}
		}
		switch {
		case strings.HasPrefix(a.Package.Ecosystem, "Debian:") && a.EcosystemSpecific.Urgency == "unimportant":
			v.Fixes[minorKey+key] = []string{DistroUnimportant}
		case strings.HasPrefix(a.Package.Ecosystem, "Ubuntu:") && negligible:
			v.Fixes[minorKey+key] = []string{DistroNegligible}
		}
		for _, r := range a.Ranges {
			for _, e := range r.Events {
				if e.Fixed != "" && !contains(v.Fixes[key], e.Fixed) {
					v.Fixes[key] = append(v.Fixes[key], e.Fixed)
				}
			}
		}
	}
	if v.Summary == "" {
		v.Summary = strings.SplitN(strings.TrimSpace(rec.Details), "\n", 2)[0]
	}
	return v
}

var cveInID = regexp.MustCompile(`CVE-\d{4}-\d{4,}`)

// CVEOf is the CVE a vulnerability ID names (CVE-…, DEBIAN-CVE-…, UBUNTU-CVE-…), or "".
func CVEOf(id string) string { return cveInID.FindString(id) }

func (o *OSV) post(ctx context.Context, path string, body []byte, into any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.BaseURL+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	return o.do(req, into)
}

func (o *OSV) do(req *http.Request, into any) error {
	resp, err := o.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("osv answered %d: %s", resp.StatusCode, strings.TrimSpace(string(b[:min(len(b), 200)])))
	}
	return json.Unmarshal(b, into)
}

// fixesFor finds the versions that fix a vulnerability for purl, under whichever name OSV files the package.
// fixesFormat marks a record's fixes read with the distributions' ratings (minorKey) and Go's packages (importsKey);
// records of an older format are read again.
const (
	fixesFormat    = "_format"
	fixesFormatNow = "3"
)

// importsKey files, beside a Go module's fixes, the packages of it that have the vulnerable code (net/http of the
// standard library): a program that links none of them is not affected.
const importsKey = "imports|"

// importsFor is the Go packages with the vulnerable code, for a Go purl; nil when the advisory does not say.
func importsFor(purl string, fixes map[string][]string) []string {
	for _, q := range osvQueries(purl) {
		for _, k := range fixKeys(q) {
			if m := fixes[importsKey+k]; len(m) > 0 {
				return m
			}
		}
	}
	return nil
}

// minorKey files, beside a package's fixes, its distribution's rating of a vulnerability it plans no security update
// for: Debian's unimportant, Ubuntu's negligible.
const minorKey = "minor|"

// minorFor is the rating purl's distribution gives the vulnerability, when it plans no security update for it.
func minorFor(purl string, fixes map[string][]string) string {
	for _, q := range osvQueries(purl) {
		for _, k := range fixKeys(q) {
			if m := fixes[minorKey+k]; len(m) > 0 {
				return m[0]
			}
		}
	}
	return ""
}

// fixKeys are the "ecosystem|package" keys a query's fixes may be filed under: Ubuntu files its LTS releases as
// Ubuntu:24.04:LTS, while an image's packages say 24.04.
func fixKeys(q osvQuery) []string {
	k := queryKey(q)
	if strings.HasPrefix(q.Package.Ecosystem, "Ubuntu:") && !strings.HasSuffix(q.Package.Ecosystem, ":LTS") {
		return []string{k, q.Package.Ecosystem + ":LTS|" + q.Package.Name}
	}
	return []string{k}
}

func fixesFor(purl string, fixes map[string][]string) []string {
	if k, ok := alasKey(purl); ok { // Amazon Linux: its advisories, not OSV's
		return fixes[k]
	}
	for _, q := range osvQueries(purl) {
		for _, k := range fixKeys(q) {
			if f := fixes[k]; len(f) > 0 {
				return f
			}
		}
	}
	return nil
}

// queryKey is the "ecosystem|package" key OSV files a query's package fixes under.
func queryKey(q osvQuery) string {
	if q.Package.Ecosystem != "" {
		return q.Package.Ecosystem + "|" + q.Package.Name
	}
	typ, rest, _ := strings.Cut(strings.TrimPrefix(q.Package.Purl, "pkg:"), "/")
	eco := map[string]string{
		"npm": "npm", "pypi": "PyPI", "golang": "Go", "maven": "Maven", "cargo": "crates.io",
		"gem": "RubyGems", "nuget": "NuGet", "composer": "Packagist", "hex": "Hex", "pub": "Pub",
	}[typ]
	name := strings.SplitN(rest, "@", 2)[0]
	if typ == "maven" {
		name = strings.Replace(name, "/", ":", 1)
	}
	if u, err := url.PathUnescape(name); err == nil {
		name = u
	}
	return eco + "|" + name
}

// distroRecord tells a distribution's record (DEBIAN-CVE-…, UBUNTU-CVE-…), which rates vulnerabilities per release.
func distroRecord(id string) bool {
	return strings.HasPrefix(id, "DEBIAN-") || strings.HasPrefix(id, "UBUNTU-") || strings.HasPrefix(id, "GO-")
}
