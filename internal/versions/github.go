// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package versions

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/pipozzz/goliash/internal/store"
)

// GitHubRelease is what Goliash reads from a GitHub release.
type GitHubRelease struct {
	Tag         string    `json:"tag_name"`
	URL         string    `json:"html_url"`
	PublishedAt time.Time `json:"published_at"`
	Draft       bool      `json:"draft"`
	Body        string    `json:"body"` // the release notes, Markdown; cut at maxNotes
}

// maxNotes is how much of one release's notes Goliash keeps: enough for any changelog worth reading, bounded so
// a hundred cached releases of many repositories stay small.
const maxNotes = 16 << 10

func cutNotes(rs []GitHubRelease) {
	for i := range rs {
		if len(rs[i].Body) > maxNotes {
			rs[i].Body = rs[i].Body[:maxNotes] + "\n…"
		}
	}
}

// GitHub lists releases of public repositories. Responses are cached for an hour and
// revalidated with ETags, which GitHub does not count against the rate limit. Set a
// token (GOLIASH_GITHUB_TOKEN) to raise the limit from 60 to 5000 requests per hour.
type GitHub struct {
	HTTP    *http.Client
	Token   string
	BaseURL string // https://api.github.com; tests point it elsewhere

	mu    sync.Mutex
	cache map[string]ghCache
}

type ghCache struct {
	etag     string
	releases []GitHubRelease
	at       time.Time
}

// NewGitHub returns a client.
func NewGitHub(token string) *GitHub {
	return &GitHub{HTTP: &http.Client{Timeout: 20 * time.Second}, Token: token, BaseURL: "https://api.github.com", cache: map[string]ghCache{}}
}

// Releases returns the newest 100 releases of owner/repo.
func (g *GitHub) Releases(ctx context.Context, repo string) ([]GitHubRelease, error) {
	g.mu.Lock()
	cached, ok := g.cache[repo]
	g.mu.Unlock()
	if ok && time.Since(cached.at) < time.Hour {
		return cached.releases, nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, g.BaseURL+"/repos/"+repo+"/releases?per_page=100", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if g.Token != "" {
		req.Header.Set("Authorization", "Bearer "+g.Token)
	}
	if cached.etag != "" {
		req.Header.Set("If-None-Match", cached.etag)
	}
	resp, err := g.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("github: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	switch resp.StatusCode {
	case http.StatusNotModified:
		cached.at = time.Now()
		g.store(repo, cached)
		return cached.releases, nil
	case http.StatusOK:
	case http.StatusForbidden, http.StatusTooManyRequests:
		return nil, fmt.Errorf("github: rate limited (%d); set GOLIASH_GITHUB_TOKEN", resp.StatusCode)
	default:
		return nil, fmt.Errorf("github: %s answered %d", repo, resp.StatusCode)
	}
	var releases []GitHubRelease
	if err := json.NewDecoder(resp.Body).Decode(&releases); err != nil {
		return nil, fmt.Errorf("github: %w", err)
	}
	cutNotes(releases)
	g.store(repo, ghCache{etag: resp.Header.Get("ETag"), releases: releases, at: time.Now()})
	return releases, nil
}

func (g *GitHub) store(repo string, c ghCache) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.cache == nil {
		g.cache = map[string]ghCache{}
	}
	g.cache[repo] = c
}

// SetGitHub enables release dates and changelog links from GitHub releases.
func (c *Checker) SetGitHub(g *GitHub) { c.github = g }

// annotateReleases fills in publication dates and changelog links of a service's
// releases from its GitHub releases or changelog template, and the security fixes each names (from its notes, or
// the project's feed). It returns the releases newly known to fix vulnerabilities. Failures only cost the
// annotation, never the check.
func (c *Checker) annotateReleases(ctx context.Context, sc store.Scope, svc store.Service, p Policy, repo string) []store.Release {
	if p.GitHub == "" && p.GitLab == "" && p.Changelog == "" && p.Security == "" {
		return nil
	}
	releases, err := c.store.ListReleases(ctx, sc, svc.ID)
	if err != nil {
		return nil
	}
	byVersion := map[string]GitHubRelease{}
	var forge []GitHubRelease
	switch {
	case p.GitHub != "" && c.github != nil:
		gh, err := c.github.Releases(ctx, p.GitHub)
		if err != nil {
			c.log.WarnContext(ctx, "github releases", "repo", p.GitHub, "err", err)
		}
		forge = gh
	case p.GitLab != "" && c.gitlab != nil:
		gl, err := c.gitlab.Releases(ctx, p.GitLab)
		if err != nil {
			c.log.WarnContext(ctx, "gitlab releases", "project", p.GitLab, "err", err)
		}
		forge = gl
	}
	{
		for _, r := range forge {
			if r.Draft {
				continue
			}
			if v, ok := ParseVersion(strings.TrimPrefix(r.Tag, p.GitHubTagPrefix)); ok {
				byVersion[versionKey(v)] = r
			}
		}
	}
	flagged := c.annotateSecurity(ctx, sc, svc, p, releases, byVersion, repo)
	for _, rel := range releases {
		if !rel.PublishedAt.IsZero() && rel.ChangelogURL != "" {
			continue
		}
		v, ok := ParseVersion(rel.Version)
		if !ok {
			continue
		}
		var published time.Time
		link := rel.ChangelogURL
		if gh, ok := byVersion[versionKey(v)]; ok {
			published, link = gh.PublishedAt, gh.URL
		} else if p.Changelog != "" {
			link = strings.ReplaceAll(p.Changelog, "{version}", versionNumber(v))
		}
		if link == rel.ChangelogURL && published.IsZero() {
			continue
		}
		if err := c.store.SetReleaseInfo(ctx, sc, svc.ID, rel.Version, published, link); err != nil {
			c.log.WarnContext(ctx, "release info", "service", svc.Name, "err", err)
		}
	}
	return flagged
}

// pagesRead bounds how many of a service's newest releases have their notes page read for security fixes.
const pagesRead = 8

// annotateSecurity records which releases fix vulnerabilities: those whose notes (on GitHub or GitLab, or their own
// notes page) name a CVE, GHSA or GO identifier or say they are security releases; and by the project's word:
// php.net's tags, Go's release history, or a release cycle in security-fixes-only support (Python, Ruby). It returns
// the releases newly flagged.
func (c *Checker) annotateSecurity(ctx context.Context, sc store.Scope, svc store.Service, p Policy, releases []store.Release, notes map[string]GitHubRelease, repo string) []store.Release {
	php := map[int]map[string]bool{}
	var goSec map[string]bool
	var cycles []EOLCycle
	cyclesRead := false
	newest := map[string]bool{} // the newest releases, whose notes pages may be read
	{
		vs := make([]Version, 0, len(releases))
		for _, r := range releases {
			if v, ok := ParseVersion(r.Version); ok {
				vs = append(vs, v)
			}
		}
		sort.Slice(vs, func(i, j int) bool { return vs[i].Compare(vs[j]) > 0 })
		for i, v := range vs {
			if i < pagesRead {
				newest[v.Raw] = true
			}
		}
	}
	var flagged []store.Release
	for _, rel := range releases {
		if rel.Security != "" {
			continue
		}
		v, ok := ParseVersion(rel.Version)
		if !ok {
			continue
		}
		sec := ""
		if r, ok := notes[versionKey(v)]; ok {
			sec = SecurityIn(r.Body)
		} else if page := changelogPage(p.Changelog, v); page != "" && newest[v.Raw] && c.text != nil {
			if text, ok := c.text.text(ctx, page); ok {
				sec = SecurityIn(text)
			}
		}
		if sec == "" && p.Security == "go.dev" && c.text != nil {
			if goSec == nil {
				goSec = c.text.goSecurity(ctx)
			}
			if goSec[versionNumber(v)] {
				sec = "security"
			}
		}
		if sec == "" && p.Security == "eol-phase" && c.eol != nil {
			if !cyclesRead {
				cyclesRead = true
				product := p.EOL
				if product == "" {
					product, _ = c.eol.Product(ctx, repo)
				}
				if product != "" && product != "none" {
					cycles, _ = c.eol.Cycles(ctx, product)
				}
			}
			if cy, ok := cycleOf(cycles, v); ok && cy.IsEOAS && !cy.IsEOL {
				sec = "security"
			}
		}
		if sec == "" && p.Security == "php.net" && c.php != nil && len(v.Parts) > 0 {
			major := v.Parts[0]
			if _, ok := php[major]; !ok {
				feed, err := c.php.securityReleases(ctx, major)
				if err != nil {
					c.log.WarnContext(ctx, "php.net releases", "err", err)
				}
				php[major] = feed
			}
			if php[major][versionNumber(v)] {
				sec = "security"
			}
		}
		if sec == "" {
			continue
		}
		if err := c.store.SetReleaseSecurity(ctx, sc, svc.ID, rel.Version, sec); err != nil {
			c.log.WarnContext(ctx, "release security", "service", svc.Name, "err", err)
			continue
		}
		rel.Security = sec
		flagged = append(flagged, rel)
	}
	return flagged
}

// versionKey identifies a version regardless of v prefix and variant ("v1.27.3", "1.27.3-alpine").
func versionKey(v Version) string {
	parts := make([]string, len(v.Parts))
	for i, p := range v.Parts {
		parts[i] = fmt.Sprint(p)
	}
	return strings.Join(parts, ".") + "-" + v.Pre
}

func versionNumber(v Version) string {
	parts := make([]string, len(v.Parts))
	for i, p := range v.Parts {
		parts[i] = fmt.Sprint(p)
	}
	return strings.Join(parts, ".")
}
