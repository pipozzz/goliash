// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package versions

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/pipozzz/goliash/internal/store"
)

// A product built from source in its image (PostgreSQL, nginx, Redis, Keycloak in their official images) is in no
// package database and often in no SBOM. NVD knows its vulnerabilities by product and version: its CVE API answers
// which CVEs a CPE ("postgresql:postgresql") at a version is affected by, with the version ranges that say where
// each is fixed. The catalog names each image's CPE.

// NVD asks the National Vulnerability Database which CVEs affect a product version. Without an API key NVD allows
// 5 requests in 30 seconds, with one 50: requests are spaced to stay under it.
type NVD struct {
	BaseURL string // https://services.nvd.nist.gov/rest/json/cves/2.0
	APIKey  string // GOLIASH_NVD_API_KEY
	HTTP    *http.Client

	mu   sync.Mutex
	last time.Time
}

// NewNVD returns a client; apiKey may be empty.
func NewNVD(apiKey string) *NVD {
	return &NVD{BaseURL: "https://services.nvd.nist.gov/rest/json/cves/2.0", APIKey: apiKey, HTTP: &http.Client{Timeout: 60 * time.Second}}
}

// gap is how long to wait between requests.
func (n *NVD) gap() time.Duration {
	if n.APIKey != "" {
		return 700 * time.Millisecond
	}
	return 6500 * time.Millisecond
}

// wait spaces requests out.
func (n *NVD) wait(ctx context.Context) error {
	n.mu.Lock()
	next := n.last.Add(n.gap())
	now := time.Now()
	if next.Before(now) {
		next = now
	}
	n.last = next
	n.mu.Unlock()
	select {
	case <-time.After(time.Until(next)):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Query lists the CVEs affecting version of product ("vendor:product"), most severe first.
func (n *NVD) Query(ctx context.Context, product, version string) ([]store.ProductVuln, error) {
	var out []store.ProductVuln
	seen := map[string]bool{}
	for start := 0; ; {
		if err := n.wait(ctx); err != nil {
			return nil, err
		}
		q := url.Values{}
		q.Set("virtualMatchString", "cpe:2.3:a:"+product+":"+version)
		q.Set("resultsPerPage", "2000")
		q.Set("startIndex", fmt.Sprint(start))
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, n.BaseURL+"?"+q.Encode(), nil)
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
		var page nvdPage
		err = json.NewDecoder(resp.Body).Decode(&page)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("NVD answered %s", resp.Status)
		}
		if err != nil {
			return nil, err
		}
		for _, v := range page.Vulnerabilities {
			if !seen[v.CVE.ID] {
				seen[v.CVE.ID] = true
				out = append(out, v.CVE.vuln(product, version))
			}
		}
		start += len(page.Vulnerabilities)
		if len(page.Vulnerabilities) == 0 || start >= page.TotalResults {
			break
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		return out[i].CVE > out[j].CVE
	})
	return out, nil
}

type nvdPage struct {
	TotalResults    int `json:"totalResults"`
	Vulnerabilities []struct {
		CVE nvdCVE `json:"cve"`
	} `json:"vulnerabilities"`
}

type nvdMetric struct {
	Type     string `json:"type"` // Primary (NVD) or Secondary (the CNA)
	CVSSData struct {
		BaseScore    float64 `json:"baseScore"`
		BaseSeverity string  `json:"baseSeverity"`
	} `json:"cvssData"`
	BaseSeverity string `json:"baseSeverity"` // CVSS 2 keeps it beside cvssData
}

type nvdCVE struct {
	ID           string `json:"id"`
	VulnStatus   string `json:"vulnStatus"`
	Descriptions []struct {
		Lang  string `json:"lang"`
		Value string `json:"value"`
	} `json:"descriptions"`
	Metrics struct {
		V40 []nvdMetric `json:"cvssMetricV40"`
		V31 []nvdMetric `json:"cvssMetricV31"`
		V30 []nvdMetric `json:"cvssMetricV30"`
		V2  []nvdMetric `json:"cvssMetricV2"`
	} `json:"metrics"`
	Configurations []struct {
		Nodes []struct {
			CPEMatch []struct {
				Vulnerable            bool   `json:"vulnerable"`
				Criteria              string `json:"criteria"`
				VersionStartIncluding string `json:"versionStartIncluding"`
				VersionStartExcluding string `json:"versionStartExcluding"`
				VersionEndIncluding   string `json:"versionEndIncluding"`
				VersionEndExcluding   string `json:"versionEndExcluding"`
			} `json:"cpeMatch"`
		} `json:"nodes"`
	} `json:"configurations"`
}

// vuln is the CVE as Goliash keeps it: summary, the severity of its newest CVSS score (NVD's own first), and the
// first fixed version of the range that holds version.
func (c nvdCVE) vuln(product, version string) store.ProductVuln {
	v := store.ProductVuln{CVE: c.ID}
	for _, d := range c.Descriptions {
		if d.Lang == "en" {
			v.Summary = d.Value
			break
		}
	}
	if len(v.Summary) > 300 {
		v.Summary = v.Summary[:300] + "…"
	}
	for _, ms := range [][]nvdMetric{c.Metrics.V40, c.Metrics.V31, c.Metrics.V30, c.Metrics.V2} {
		if len(ms) == 0 {
			continue
		}
		m := ms[0]
		for _, x := range ms {
			if x.Type == "Primary" {
				m = x
				break
			}
		}
		v.Score, v.Severity = m.CVSSData.BaseScore, strings.ToUpper(m.CVSSData.BaseSeverity)
		if v.Severity == "" {
			v.Severity = strings.ToUpper(m.BaseSeverity)
		}
		break
	}
	running, ok := ParseVersion(version)
	if !ok {
		return v
	}
	prefix := "cpe:2.3:a:" + product + ":"
	for _, cfg := range c.Configurations {
		for _, n := range cfg.Nodes {
			for _, m := range n.CPEMatch {
				if !m.Vulnerable || !strings.HasPrefix(m.Criteria, prefix) || m.VersionEndExcluding == "" {
					continue
				}
				end, ok := ParseVersion(m.VersionEndExcluding)
				if !ok || running.Compare(end) >= 0 || !atLeast(running, m.VersionStartIncluding, false) || !atLeast(running, m.VersionStartExcluding, true) {
					continue
				}
				if f, ok := ParseVersion(v.Fix); v.Fix == "" || !ok || end.Compare(f) < 0 {
					v.Fix = m.VersionEndExcluding
				}
			}
		}
	}
	return v
}

// atLeast reports whether v is at or after start (strictly after when exclusive); an empty start holds.
func atLeast(v Version, start string, exclusive bool) bool {
	if start == "" {
		return true
	}
	s, ok := ParseVersion(start)
	if !ok {
		return true
	}
	if exclusive {
		return v.Compare(s) > 0
	}
	return v.Compare(s) >= 0
}
