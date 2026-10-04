// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package versions

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// eolWarning is how long before its end of life a release cycle is reported.
const eolWarning = 60 * 24 * time.Hour

// EOLCycle is one release cycle of a product, e.g. PostgreSQL 15.
type EOLCycle struct {
	Name    string
	EOLFrom time.Time // zero when no date is known
	IsEOL   bool
}

// EOL reads end-of-life dates from endoflife.date: which product an image is (from the
// site's package URLs, e.g. pkg:docker/library/postgres) and when each release cycle
// of a product stops being supported. Responses are cached for a day.
type EOL struct {
	HTTP    *http.Client
	BaseURL string // https://endoflife.date/api/v1; tests point it elsewhere

	mu       sync.Mutex
	products map[string]string // image repository -> product
	loaded   time.Time
	cycles   map[string]eolEntry
}

type eolEntry struct {
	cycles []EOLCycle
	at     time.Time
}

// NewEOL returns a client for endoflife.date.
func NewEOL() *EOL {
	return &EOL{HTTP: &http.Client{Timeout: 20 * time.Second}, BaseURL: "https://endoflife.date/api/v1", cycles: map[string]eolEntry{}}
}

// SetEOL enables end-of-life drift.
func (c *Checker) SetEOL(e *EOL) { c.eol = e }

// Product returns the endoflife.date product of an image repository
// ("docker.io/library/postgres" -> "postgresql"), or "" when the site does not list it.
func (e *EOL) Product(ctx context.Context, repo string) (string, error) {
	e.mu.Lock()
	fresh := e.products != nil && time.Since(e.loaded) < 24*time.Hour
	e.mu.Unlock()
	if !fresh {
		var out struct {
			Result []struct {
				Identifier string `json:"identifier"`
				Product    struct {
					Name string `json:"name"`
				} `json:"product"`
			} `json:"result"`
		}
		if err := e.get(ctx, "/identifiers/purl", &out); err != nil {
			return "", err
		}
		products := map[string]string{}
		for _, r := range out.Result {
			if image := purlImage(r.Identifier); image != "" {
				products[image] = r.Product.Name
			}
		}
		e.mu.Lock()
		e.products, e.loaded = products, time.Now()
		e.mu.Unlock()
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.products[repo], nil
}

// purlImage turns a package URL for a container image into Goliash's repository form:
// pkg:docker/library/postgres -> docker.io/library/postgres,
// pkg:docker/bitnami/redis -> docker.io/bitnami/redis,
// pkg:oci/airflow?repository_url=cgr.dev/chainguard -> cgr.dev/chainguard/airflow.
func purlImage(purl string) string {
	rest, ok := strings.CutPrefix(purl, "pkg:docker/")
	if ok {
		name, query, _ := strings.Cut(rest, "?")
		name, _, _ = strings.Cut(name, "@")
		if q, err := url.ParseQuery(query); err == nil && q.Get("repository_url") != "" {
			return strings.TrimSuffix(q.Get("repository_url"), "/") + "/" + name
		}
		if !strings.Contains(name, "/") {
			name = "library/" + name
		}
		return "docker.io/" + name
	}
	if rest, ok = strings.CutPrefix(purl, "pkg:oci/"); ok {
		name, query, _ := strings.Cut(rest, "?")
		name, _, _ = strings.Cut(name, "@")
		q, err := url.ParseQuery(query)
		if err != nil || q.Get("repository_url") == "" {
			return ""
		}
		repo := strings.TrimSuffix(q.Get("repository_url"), "/")
		if strings.HasSuffix(repo, "/"+name) {
			return repo
		}
		return repo + "/" + name
	}
	return ""
}

// Cycles returns the release cycles of a product.
func (e *EOL) Cycles(ctx context.Context, product string) ([]EOLCycle, error) {
	e.mu.Lock()
	c, ok := e.cycles[product]
	e.mu.Unlock()
	if ok && time.Since(c.at) < 24*time.Hour {
		return c.cycles, nil
	}
	var out struct {
		Result struct {
			Releases []struct {
				Name    string `json:"name"`
				IsEOL   bool   `json:"isEol"`
				EOLFrom string `json:"eolFrom"`
			} `json:"releases"`
		} `json:"result"`
	}
	if err := e.get(ctx, "/products/"+url.PathEscape(product), &out); err != nil {
		return nil, err
	}
	cycles := make([]EOLCycle, 0, len(out.Result.Releases))
	for _, r := range out.Result.Releases {
		cy := EOLCycle{Name: r.Name, IsEOL: r.IsEOL}
		if t, err := time.Parse("2006-01-02", r.EOLFrom); err == nil {
			cy.EOLFrom = t
		}
		cycles = append(cycles, cy)
	}
	e.mu.Lock()
	e.cycles[product] = eolEntry{cycles: cycles, at: time.Now()}
	e.mu.Unlock()
	return cycles, nil
}

func (e *EOL) get(ctx context.Context, path string, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, e.BaseURL+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := e.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("endoflife.date: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("endoflife.date %s answered %d", path, resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(v)
}

// CycleFor picks the release cycle a version belongs to: the cycle whose name matches
// the most leading parts of the version (15.6 -> "15", 7.2.4 -> "7.2", 1.27.3 -> "1.27").
func CycleFor(tag string, cycles []EOLCycle) (EOLCycle, bool) {
	v, ok := ParseVersion(tag)
	if !ok {
		return EOLCycle{}, false
	}
	var best EOLCycle
	bestParts := 0
	for _, c := range cycles {
		cv, ok := ParseVersion(c.Name)
		if !ok || len(cv.Parts) > len(v.Parts) || len(cv.Parts) <= bestParts {
			continue
		}
		match := true
		for i, p := range cv.Parts {
			if v.Parts[i] != p {
				match = false
				break
			}
		}
		if match {
			best, bestParts = c, len(cv.Parts)
		}
	}
	return best, bestParts > 0
}

// EOLDrifts lists cells whose running release cycle has reached its end of life, or
// will within eolWarning.
func EOLDrifts(m Matrix, cycles map[string][]EOLCycle, now time.Time) []WantedDrift {
	var out []WantedDrift
	for _, row := range m.Rows {
		cs := cycles[row.Service.ID]
		if len(cs) == 0 {
			continue
		}
		for ei, cell := range row.Cells {
			if cell.Empty() {
				continue
			}
			running := cell.Primary().Tag
			c, ok := CycleFor(running, cs)
			if !ok {
				continue
			}
			ended := c.IsEOL || (!c.EOLFrom.IsZero() && !c.EOLFrom.After(now))
			soon := !c.EOLFrom.IsZero() && c.EOLFrom.Sub(now) <= eolWarning
			if !ended && !soon {
				continue
			}
			d := DriftDetail{Running: running, Other: c.Name}
			if !c.EOLFrom.IsZero() {
				d.EOL = c.EOLFrom.Format("2006-01-02")
			}
			out = append(out, WantedDrift{row.Service.ID, m.Environments[ei].ID, "eol", d})
		}
	}
	return out
}
