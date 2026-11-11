// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package versions

import (
	"bufio"
	"compress/gzip"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/pipozzz/goliash/internal/store"
)

// Amazon Linux's security advisories (ALAS), which OSV does not have: each release's core repository publishes
// them as updateinfo.xml, with the CVEs each fixes and the exact package versions that fix it.
var alasMirrors = map[string]string{
	"2023": "https://cdn.amazonlinux.com/al2023/core/mirrors/latest/x86_64/mirror.list",
	"2":    "https://cdn.amazonlinux.com/2/core/latest/x86_64/mirror.list",
}

// alasRefresh is how often a release's advisories are read again.
const alasRefresh = 24 * time.Hour

// alasEcosystem is the "ecosystem" Amazon Linux's fixes are filed under, beside OSV's: "Amazon Linux:2023".
const alasEcosystem = "Amazon Linux:"

// alasFix is a package version an advisory says fixes it.
type alasFix struct {
	id  string
	evr string // epoch:version-release
}

// alasIndex is one release's advisories: the fixes by binary package, and each advisory as a vulnerability record.
type alasIndex struct {
	read  time.Time
	fixes map[string][]alasFix
	recs  map[string]store.Vuln
}

// alasCache keeps each release's advisories between runs.
type alasCache struct {
	mu   sync.Mutex
	byRe map[string]*alasIndex
	// mirrors and http are what is read; tests point them elsewhere.
	mirrors map[string]string
	http    *http.Client
}

var alas = &alasCache{byRe: map[string]*alasIndex{}, mirrors: alasMirrors, http: &http.Client{Timeout: 3 * time.Minute}}

// amazonPurl reads an Amazon Linux package URL: its release (2023, 2), name, and epoch:version-release.
func amazonPurl(purl string) (release, name, evr string, ok bool) {
	rest, found := strings.CutPrefix(purl, "pkg:rpm/amzn/")
	if !found {
		rest, found = strings.CutPrefix(purl, "pkg:rpm/amazon/")
	}
	if !found {
		return "", "", "", false
	}
	path, query, _ := strings.Cut(rest, "?")
	name, version, ok := strings.Cut(path, "@")
	if !ok {
		return "", "", "", false
	}
	if v, err := url.PathUnescape(version); err == nil {
		version = v
	}
	qs, _ := url.ParseQuery(query)
	release = qs.Get("os_version")
	if release == "" {
		d := qs.Get("distro") // amzn-2023 as SBOM tools write it
		release = d[strings.LastIndex(d, "-")+1:]
	}
	release, _, _ = strings.Cut(release, ".")
	if _, known := alasMirrors[release]; !known {
		return "", "", "", false
	}
	epoch := qs.Get("epoch")
	if epoch == "" {
		epoch = "0"
	}
	if strings.Contains(version, ":") { // an epoch written into the version
		return release, name, version, true
	}
	return release, name, epoch + ":" + version, true
}

// alasKey is where an Amazon Linux package's fixes are filed in a record's Fixes.
func alasKey(purl string) (string, bool) {
	release, name, _, ok := amazonPurl(purl)
	if !ok {
		return "", false
	}
	return alasEcosystem + release + "|" + name, true
}

// amazonLinuxVulns finds the advisories of Amazon Linux packages among purls (installed older than a version an
// advisory says fixes it) and records the advisories met. It returns, for each such package, the advisory IDs;
// packages of other distributions are not in it.
func (c *Checker) amazonLinuxVulns(ctx context.Context, purls []string) (map[string][]string, error) {
	byRelease := map[string][]string{}
	for _, p := range purls {
		if r, _, _, ok := amazonPurl(p); ok {
			byRelease[r] = append(byRelease[r], p)
		}
	}
	out := map[string][]string{}
	var errs []error
	for release, ps := range byRelease {
		idx, err := alas.index(ctx, release)
		if err != nil {
			errs = append(errs, fmt.Errorf("amazon linux %s advisories: %w", release, err))
			continue
		}
		met := map[string]bool{}
		for _, p := range ps {
			_, name, evr, _ := amazonPurl(p)
			ids := []string{}
			for _, f := range idx.fixes[name] {
				if rpmCompare(evr, f.evr) < 0 && !contains(ids, f.id) {
					ids = append(ids, f.id)
					met[f.id] = true
				}
			}
			out[p] = ids
		}
		for id := range met {
			if err := c.store.SetVuln(ctx, idx.recs[id]); err != nil {
				return out, err
			}
		}
	}
	return out, errors.Join(errs...)
}

// index is a release's advisories, read again once a day.
func (a *alasCache) index(ctx context.Context, release string) (*alasIndex, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if idx := a.byRe[release]; idx != nil && time.Since(idx.read) < alasRefresh {
		return idx, nil
	}
	idx, err := a.fetch(ctx, release)
	if err != nil {
		if old := a.byRe[release]; old != nil {
			return old, nil // yesterday's advisories rather than none
		}
		return nil, err
	}
	a.byRe[release] = idx
	return idx, nil
}

func (a *alasCache) get(ctx context.Context, u string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, http.NoBody)
	if err != nil {
		return nil, err
	}
	resp, err := a.http.Do(req) //nolint:gosec // Amazon's repository, or a test's
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		return nil, fmt.Errorf("%s: %s", u, resp.Status)
	}
	return resp, nil
}

// fetch reads a release's updateinfo: the mirror list names the repository, its repomd.xml the updateinfo file.
func (a *alasCache) fetch(ctx context.Context, release string) (*alasIndex, error) {
	resp, err := a.get(ctx, a.mirrors[release])
	if err != nil {
		return nil, err
	}
	line, err := bufio.NewReader(io.LimitReader(resp.Body, 4096)).ReadString('\n')
	_ = resp.Body.Close()
	base := strings.TrimSpace(line)
	if base == "" {
		return nil, fmt.Errorf("the mirror list names no repository (%w)", err)
	}
	if !strings.HasSuffix(base, "/") {
		base += "/"
	}
	resp, err = a.get(ctx, base+"repodata/repomd.xml")
	if err != nil {
		return nil, err
	}
	var md struct {
		Data []struct {
			Type     string `xml:"type,attr"`
			Location struct {
				Href string `xml:"href,attr"`
			} `xml:"location"`
		} `xml:"data"`
	}
	err = xml.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&md)
	_ = resp.Body.Close()
	if err != nil {
		return nil, err
	}
	href := ""
	for _, d := range md.Data {
		if d.Type == "updateinfo" {
			href = d.Location.Href
		}
	}
	if href == "" {
		return nil, errors.New("the repository lists no updateinfo")
	}
	resp, err = a.get(ctx, base+href)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	body := io.LimitReader(resp.Body, 64<<20)
	if strings.HasSuffix(href, ".gz") {
		gz, err := gzip.NewReader(body)
		if err != nil {
			return nil, err
		}
		body = io.LimitReader(gz, 512<<20)
	}
	return parseUpdateinfo(body, release)
}

// parseUpdateinfo reads security advisories from updateinfo.xml, one <update> at a time.
func parseUpdateinfo(r io.Reader, release string) (*alasIndex, error) {
	idx := &alasIndex{read: time.Now(), fixes: map[string][]alasFix{}, recs: map[string]store.Vuln{}}
	dec := xml.NewDecoder(r)
	type update struct {
		Type     string `xml:"type,attr"`
		ID       string `xml:"id"`
		Title    string `xml:"title"`
		Severity string `xml:"severity"`
		Refs     []struct {
			ID   string `xml:"id,attr"`
			Type string `xml:"type,attr"`
		} `xml:"references>reference"`
		Packages []struct {
			Name    string `xml:"name,attr"`
			Epoch   string `xml:"epoch,attr"`
			Version string `xml:"version,attr"`
			Release string `xml:"release,attr"`
		} `xml:"pkglist>collection>package"`
	}
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		start, ok := tok.(xml.StartElement)
		if !ok || start.Name.Local != "update" {
			continue
		}
		var u update
		if err := dec.DecodeElement(&u, &start); err != nil {
			return nil, err
		}
		if u.Type != "security" || u.ID == "" {
			continue
		}
		rec := store.Vuln{ID: u.ID, Summary: u.Title, Severity: alasSeverity(u.Severity), Fixes: map[string][]string{fixesFormat: {fixesFormatNow}}}
		for _, ref := range u.Refs {
			if ref.Type == "cve" && strings.HasPrefix(ref.ID, "CVE-") && !contains(rec.Aliases, ref.ID) {
				rec.Aliases = append(rec.Aliases, ref.ID)
			}
		}
		for _, p := range u.Packages {
			if p.Name == "" || p.Version == "" || strings.HasSuffix(p.Name, "-debuginfo") || strings.HasSuffix(p.Name, "-debugsource") {
				continue
			}
			epoch := p.Epoch
			if epoch == "" {
				epoch = "0"
			}
			evr := epoch + ":" + p.Version + "-" + p.Release
			key := alasEcosystem + release + "|" + p.Name
			if !contains(rec.Fixes[key], evr) {
				rec.Fixes[key] = append(rec.Fixes[key], evr)
				idx.fixes[p.Name] = append(idx.fixes[p.Name], alasFix{u.ID, evr})
			}
		}
		idx.recs[u.ID] = rec
	}
	if len(idx.recs) == 0 {
		return nil, errors.New("updateinfo has no security advisory")
	}
	return idx, nil
}

// alasSeverity maps Amazon's priorities to OSV's severities.
func alasSeverity(s string) string {
	switch strings.ToLower(s) {
	case "critical":
		return "CRITICAL"
	case "important":
		return "HIGH"
	case "medium":
		return "MODERATE"
	case "low":
		return "LOW"
	}
	return ""
}

// rpmCompare orders two epoch:version-release strings as rpm does: -1, 0 or 1.
func rpmCompare(a, b string) int {
	ea, va, ra := splitEVR(a)
	eb, vb, rb := splitEVR(b)
	if c := rpmVerCmp(ea, eb); c != 0 {
		return c
	}
	if c := rpmVerCmp(va, vb); c != 0 {
		return c
	}
	return rpmVerCmp(ra, rb)
}

func splitEVR(s string) (epoch, version, release string) {
	epoch = "0"
	if e, rest, ok := strings.Cut(s, ":"); ok {
		epoch, s = e, rest
	}
	if i := strings.LastIndex(s, "-"); i >= 0 {
		return epoch, s[:i], s[i+1:]
	}
	return epoch, s, ""
}

// rpmVerCmp is rpm's rpmvercmp: segments of digits compared as numbers, of letters as strings, a digit segment
// newer than a letter one, ~ before anything (a pre-release), ^ after the end (a post-release snapshot).
func rpmVerCmp(a, b string) int {
	if a == b {
		return 0
	}
	alnum := func(r byte) bool { return r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' }
	digit := func(r byte) bool { return r >= '0' && r <= '9' }
	i, j := 0, 0
	for i < len(a) || j < len(b) {
		for i < len(a) && !alnum(a[i]) && a[i] != '~' && a[i] != '^' {
			i++
		}
		for j < len(b) && !alnum(b[j]) && b[j] != '~' && b[j] != '^' {
			j++
		}
		// ~ sorts before everything, even the end.
		if i < len(a) && a[i] == '~' || j < len(b) && b[j] == '~' {
			if i >= len(a) || a[i] != '~' {
				return 1
			}
			if j >= len(b) || b[j] != '~' {
				return -1
			}
			i, j = i+1, j+1
			continue
		}
		// ^ sorts after the end, before anything else.
		if i < len(a) && a[i] == '^' || j < len(b) && b[j] == '^' {
			if i >= len(a) {
				return -1
			}
			if j >= len(b) {
				return 1
			}
			if a[i] != '^' {
				return 1
			}
			if b[j] != '^' {
				return -1
			}
			i, j = i+1, j+1
			continue
		}
		if i >= len(a) || j >= len(b) {
			break
		}
		si, sj := i, j
		isNum := digit(a[i])
		if isNum {
			for i < len(a) && digit(a[i]) {
				i++
			}
			for j < len(b) && digit(b[j]) {
				j++
			}
		} else {
			for i < len(a) && alnum(a[i]) && !digit(a[i]) {
				i++
			}
			for j < len(b) && alnum(b[j]) && !digit(b[j]) {
				j++
			}
		}
		x, y := a[si:i], b[sj:j]
		if y == "" { // segments of different kinds: the number is newer
			if isNum {
				return 1
			}
			return -1
		}
		if isNum {
			x, y = strings.TrimLeft(x, "0"), strings.TrimLeft(y, "0")
			if len(x) != len(y) {
				if len(x) > len(y) {
					return 1
				}
				return -1
			}
		}
		if x != y {
			if x > y {
				return 1
			}
			return -1
		}
	}
	switch {
	case i >= len(a) && j >= len(b):
		return 0
	case i >= len(a):
		return -1
	}
	return 1
}

// CompareDistroVersions orders two package versions of a distribution (dpkg's or rpm's, epoch:version-release), as
// rpm does: -1, 0 or 1. Close enough for Debian's too, ~ included.
func CompareDistroVersions(a, b string) int { return rpmCompare(a, b) }
