// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package versions

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/pipozzz/goliash/internal/store"
)

// Many projects publish their security advisories in their own GitHub repository (Redis, Keycloak, Grafana,
// Traefik, Envoy, RabbitMQ, NATS…), with the versions each affects and the versions that fix it, as the authors
// say. Where a product's repository has any, they are its source; NVD is for the others.

// GitHubAdvisory is a published repository security advisory.
type GitHubAdvisory struct {
	GHSA      string    `json:"ghsa_id"`
	CVE       string    `json:"cve_id"`
	Published time.Time `json:"published_at"`
	Summary   string    `json:"summary"`
	Severity  string    `json:"severity"`
	CVSS      struct {
		V3 struct {
			Score float64 `json:"score"`
		} `json:"cvss_v3"`
		V4 struct {
			Score float64 `json:"score"`
		} `json:"cvss_v4"`
	} `json:"cvss_severities"`
	Vulnerabilities []struct {
		Range   string `json:"vulnerable_version_range"`
		Patched string `json:"patched_versions"`
	} `json:"vulnerabilities"`
}

type ghaCache struct {
	list []GitHubAdvisory
	at   time.Time
}

var (
	ghaMu     sync.Mutex
	ghaCached = map[string]ghaCache{}
)

// advisoryTTL is how long a repository's advisories are kept before they are read again.
const advisoryTTL = 6 * time.Hour

// Advisories lists the published security advisories of a repository ("owner/repo").
func (g *GitHub) Advisories(ctx context.Context, repo string) ([]GitHubAdvisory, error) {
	ghaMu.Lock()
	c, ok := ghaCached[g.BaseURL+"|"+repo]
	ghaMu.Unlock()
	if ok && time.Since(c.at) < advisoryTTL {
		return c.list, nil
	}
	var all []GitHubAdvisory
	for page := 1; page <= 10; page++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("%s/repos/%s/security-advisories?state=published&per_page=100&page=%d", g.BaseURL, repo, page), nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Accept", "application/vnd.github+json")
		req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
		if g.Token != "" {
			req.Header.Set("Authorization", "Bearer "+g.Token)
		}
		resp, err := g.HTTP.Do(req)
		if err != nil {
			return nil, fmt.Errorf("github: %w", err)
		}
		var list []GitHubAdvisory
		switch resp.StatusCode {
		case http.StatusOK:
			err = json.NewDecoder(resp.Body).Decode(&list)
		case http.StatusNotFound:
		case http.StatusForbidden, http.StatusTooManyRequests:
			err = fmt.Errorf("github: rate limited (%d); set GOLIASH_GITHUB_TOKEN", resp.StatusCode)
		default:
			err = fmt.Errorf("github: %s advisories answered %d", repo, resp.StatusCode)
		}
		_ = resp.Body.Close()
		if err != nil {
			return nil, err
		}
		all = append(all, list...)
		if len(list) < 100 {
			break
		}
	}
	ghaMu.Lock()
	ghaCached[g.BaseURL+"|"+repo] = ghaCache{list: all, at: time.Now()}
	ghaMu.Unlock()
	return all, nil
}

// staleUnfixed is how old an advisory with no patched version and no upper bound may be and still count: a
// project that never filled in the fix would otherwise mark every later release affected.
const staleUnfixed = 180 * 24 * time.Hour

// AdvisoryVulns are the advisories that affect version, as Goliash keeps them: the CVE (else the GHSA), summary,
// severity and the version that fixes it on the running release line.
func AdvisoryVulns(list []GitHubAdvisory, version string, now time.Time) []store.ProductVuln {
	running, ok := ParseVersion(version)
	if !ok {
		return nil
	}
	var out []store.ProductVuln
	for _, a := range list {
		fix, hit := "", false
		for _, v := range a.Vulnerabilities {
			if !versionWord.MatchString(v.Patched) && !rangeUpper.MatchString(v.Range) && !a.Published.IsZero() && now.Sub(a.Published) > staleUnfixed {
				continue
			}
			if f, affected := affects(running, v.Range, v.Patched); affected {
				hit = true
				if fix == "" {
					fix = f
				}
			}
		}
		if !hit {
			continue
		}
		id := a.CVE
		if id == "" {
			id = a.GHSA
		}
		pv := store.ProductVuln{CVE: id, Summary: a.Summary, Severity: strings.ToUpper(a.Severity), Score: a.CVSS.V4.Score, Fix: fix}
		if pv.Score == 0 {
			pv.Score = a.CVSS.V3.Score
		}
		out = append(out, pv)
	}
	sort.Slice(out, func(i, j int) bool {
		if severityRank(out[i].Severity) != severityRank(out[j].Severity) {
			return severityRank(out[i].Severity) > severityRank(out[j].Severity)
		}
		return out[i].CVE > out[j].CVE
	})
	return out
}

var (
	rangeBound  = regexp.MustCompile(`(>=|<=|>|<|=)\s*v?(\d[0-9A-Za-z.+-]*)`)
	rangeDash   = regexp.MustCompile(`v?(\d[0-9A-Za-z.+-]*)\s+-\s+v?(\d[0-9A-Za-z.+-]*)`)
	rangeEarier = regexp.MustCompile(`v?(\d[0-9A-Za-z.+-]*)\s+and\s+earlier`)
	versionWord = regexp.MustCompile(`v?(\d+(?:\.\d+)+[0-9A-Za-z.+-]*)`)
	rangeUpper  = regexp.MustCompile(`<|\s-\s|and\s+earlier`)
)

// versionBound is one side of an interval: ">= 3.0.0".
type versionBound struct {
	op string
	v  Version
}

// affects reads an advisory's affected range and patched versions, as projects write them ("< 26.4.14",
// ">= 3.0.0, <= 3.7.15", ">=9.2.0 <9.2.10 >=9.3.0 <9.3.4", "6.3.0-beta1 - 9.3.1", "All"; "6.2.20, 7.2.11",
// "TBD"), and says whether running is affected and what fixes it on its release line. A patched version on the
// running release line decides (8.2.1 is affected when 8.2.3 is patched, 7.4.6 is not when 7.4.6 is); else the
// range does, and the fix is the first patched version after running.
func affects(running Version, rng, patched string) (string, bool) {
	var fixes []Version
	for _, w := range versionWord.FindAllStringSubmatch(patched, -1) {
		if v, ok := ParseVersion(w[1]); ok {
			fixes = append(fixes, v)
		}
	}
	sort.Slice(fixes, func(i, j int) bool { return fixes[i].Compare(fixes[j]) < 0 })
	for _, f := range fixes {
		if sameLine(f, running) {
			if running.Compare(f) >= 0 {
				return "", false
			}
			return f.Raw, inRange(running, rng, true)
		}
	}
	if len(fixes) > 0 && running.Compare(fixes[len(fixes)-1]) > 0 {
		return "", false // a release line after every fixed one
	}
	if !inRange(running, rng, false) {
		return "", false
	}
	for _, f := range fixes {
		if f.Compare(running) > 0 {
			return f.Raw, true
		}
	}
	return "", true
}

// sameLine reports whether two versions are of one release line: the same major and minor.
func sameLine(a, b Version) bool {
	return len(a.Parts) >= 2 && len(b.Parts) >= 2 && a.Parts[0] == b.Parts[0] && a.Parts[1] == b.Parts[1]
}

// inRange reads a range as intervals: a lower bound opens one, an upper bound closes it; "a - b" and "a and
// earlier" are intervals too, "All" is everything. A range Goliash cannot read holds when lenient.
func inRange(v Version, rng string, lenient bool) bool {
	r := strings.TrimSpace(rng)
	if r == "" || strings.EqualFold(r, "all") || strings.HasPrefix(strings.ToLower(r), "all ") {
		return true
	}
	matched := false
	for _, m := range rangeDash.FindAllStringSubmatch(r, -1) {
		matched = true
		lo, ok1 := ParseVersion(m[1])
		hi, ok2 := ParseVersion(m[2])
		if ok1 && ok2 && v.Compare(lo) >= 0 && v.Compare(hi) <= 0 {
			return true
		}
	}
	for _, m := range rangeEarier.FindAllStringSubmatch(r, -1) {
		matched = true
		if hi, ok := ParseVersion(m[1]); ok && v.Compare(hi) <= 0 {
			return true
		}
	}
	var lower *versionBound
	open := false
	for _, m := range rangeBound.FindAllStringSubmatch(rangeDash.ReplaceAllString(r, ""), -1) {
		b, ok := ParseVersion(m[2])
		if !ok {
			continue
		}
		matched = true
		switch m[1] {
		case ">=", ">":
			if open && lower != nil && holds(v, *lower) { // an interval left open: everything after it
				return true
			}
			lower, open = &versionBound{m[1], b}, true
		case "<", "<=":
			if (lower == nil || holds(v, *lower)) && holds(v, versionBound{m[1], b}) {
				return true
			}
			lower, open = nil, false
		case "=":
			if v.Compare(b) == 0 {
				return true
			}
		}
	}
	if open && lower != nil && holds(v, *lower) {
		return true
	}
	return !matched && lenient
}

func holds(v Version, b versionBound) bool {
	c := v.Compare(b.v)
	switch b.op {
	case ">=":
		return c >= 0
	case ">":
		return c > 0
	case "<=":
		return c <= 0
	case "<":
		return c < 0
	}
	return c == 0
}
