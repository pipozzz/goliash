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
	"time"
)

// OSV asks osv.dev (or a mirror) which known vulnerabilities affect packages. Only package names and
// versions are sent, as package URLs or ecosystem, name and version.
type OSV struct {
	HTTP    *http.Client
	BaseURL string // https://api.osv.dev/v1
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
	Version string `json:"version,omitempty"`
}

// osvQueryFor turns a package URL into an OSV query: Debian, Ubuntu and Alpine packages by ecosystem
// ("Debian:12"), name and version, others (npm, PyPI, Go, Maven …) by their purl without qualifiers.
func osvQueryFor(purl string) (osvQuery, bool) {
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
			q.Package.Name = strings.SplitN(src, "@", 2)[0]
		}
	case "apk":
		if osVersion == "" {
			return q, false
		}
		parts := strings.SplitN(osVersion, ".", 3)
		if len(parts) < 2 {
			return q, false
		}
		q.Package.Ecosystem = "Alpine:v" + parts[0] + "." + parts[1]
		q.Package.Name, q.Version = name, version
	default:
		q.Package.Purl = strings.SplitN(purl, "?", 2)[0]
	}
	return q, true
}

// QueryBatch returns the IDs of the vulnerabilities affecting each package URL; packages OSV cannot be asked
// about are left out.
func (o *OSV) QueryBatch(ctx context.Context, purls []string) (map[string][]string, error) {
	var queries []osvQuery
	var asked []string
	for _, p := range purls {
		if q, ok := osvQueryFor(p); ok {
			queries = append(queries, q)
			asked = append(asked, p)
		}
	}
	out := map[string][]string{}
	for start := 0; start < len(queries); start += 1000 {
		end := min(start+1000, len(queries))
		body, _ := json.Marshal(map[string]any{"queries": queries[start:end]})
		var res struct {
			Results []struct {
				Vulns []struct {
					ID string `json:"id"`
				} `json:"vulns"`
			} `json:"results"`
		}
		if err := o.post(ctx, "/querybatch", body, &res); err != nil {
			return nil, err
		}
		for i, r := range res.Results {
			if start+i >= len(asked) {
				break
			}
			ids := []string{}
			for _, v := range r.Vulns {
				ids = append(ids, v.ID)
			}
			out[asked[start+i]] = ids
		}
	}
	return out, nil
}

// OSVVuln is what Goliash keeps of a vulnerability.
type OSVVuln struct {
	ID       string
	Aliases  []string
	Summary  string
	Severity string              // CRITICAL, HIGH, MODERATE, LOW when the record says; empty otherwise
	Fixes    map[string][]string // "ecosystem|package" -> versions that fix it
}

// Vuln reads one vulnerability record.
func (o *OSV) Vuln(ctx context.Context, id string) (OSVVuln, error) {
	var rec struct {
		ID               string   `json:"id"`
		Aliases          []string `json:"aliases"`
		Upstream         []string `json:"upstream"`
		Summary          string   `json:"summary"`
		Details          string   `json:"details"`
		DatabaseSpecific struct {
			Severity string `json:"severity"`
		} `json:"database_specific"`
		Affected []struct {
			Package struct {
				Name      string `json:"name"`
				Ecosystem string `json:"ecosystem"`
			} `json:"package"`
			Ranges []struct {
				Events []struct {
					Fixed string `json:"fixed"`
				} `json:"events"`
			} `json:"ranges"`
		} `json:"affected"`
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, o.BaseURL+"/vulns/"+url.PathEscape(id), nil)
	if err != nil {
		return OSVVuln{}, err
	}
	if err := o.do(req, &rec); err != nil {
		return OSVVuln{}, err
	}
	v := OSVVuln{ID: rec.ID, Aliases: append(rec.Aliases, rec.Upstream...), Summary: rec.Summary, Severity: strings.ToUpper(rec.DatabaseSpecific.Severity), Fixes: map[string][]string{}}
	for _, a := range rec.Affected {
		key := a.Package.Ecosystem + "|" + a.Package.Name
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
	return v, nil
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

// fixKey is the "ecosystem|package" key OSV files a package's fixes under.
func fixKey(purl string) string {
	q, ok := osvQueryFor(purl)
	if !ok {
		return ""
	}
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
