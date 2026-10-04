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

// GitLab lists releases of GitLab projects on gitlab.com and, when configured, one
// self-hosted instance. Other hosts are refused: a policy or an image label must not make
// the server call arbitrary addresses or send the token there. Responses are cached for
// an hour.
type GitLab struct {
	HTTP  *http.Client
	hosts map[string]gitlabHost // host -> API base URL and token

	mu    sync.Mutex
	cache map[string]glCache
}

type gitlabHost struct {
	api   string
	token string
}

type glCache struct {
	releases []GitHubRelease
	at       time.Time
}

// NewGitLab returns a client for gitlab.com and, when selfURL is set (e.g.
// https://gitlab.example.com), that instance. The token is sent to the self-hosted
// instance, or to gitlab.com when there is none.
func NewGitLab(selfURL, token string) *GitLab {
	g := &GitLab{
		HTTP:  &http.Client{Timeout: 20 * time.Second},
		hosts: map[string]gitlabHost{"gitlab.com": {api: "https://gitlab.com/api/v4"}},
		cache: map[string]glCache{},
	}
	if u, err := url.Parse(strings.TrimRight(selfURL, "/")); err == nil && u.Host != "" && (u.Scheme == "https" || u.Scheme == "http") {
		g.hosts[u.Host] = gitlabHost{api: u.Scheme + "://" + u.Host + u.Path + "/api/v4", token: token}
	} else if token != "" {
		g.hosts["gitlab.com"] = gitlabHost{api: "https://gitlab.com/api/v4", token: token}
	}
	gitlabHosts.Store(g.Hosts())
	return g
}

// Hosts lists the GitLab hosts the client may call.
func (g *GitLab) Hosts() []string {
	out := make([]string, 0, len(g.hosts))
	for h := range g.hosts {
		out = append(out, h)
	}
	return out
}

// Releases returns the newest 100 releases of "host/group/project".
func (g *GitLab) Releases(ctx context.Context, project string) ([]GitHubRelease, error) {
	hostName, path, ok := strings.Cut(project, "/")
	host, allowed := g.hosts[hostName]
	if !ok || !allowed || path == "" {
		return nil, fmt.Errorf("gitlab: %s is not on an allowed host (gitlab.com or GOLIASH_GITLAB_URL)", project)
	}
	g.mu.Lock()
	cached, hit := g.cache[project]
	g.mu.Unlock()
	if hit && time.Since(cached.at) < time.Hour {
		return cached.releases, nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		host.api+"/projects/"+url.PathEscape(path)+"/releases?per_page=100", nil)
	if err != nil {
		return nil, err
	}
	if host.token != "" {
		req.Header.Set("PRIVATE-TOKEN", host.token)
	}
	resp, err := g.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("gitlab: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusTooManyRequests:
		return nil, fmt.Errorf("gitlab: rate limited; set GOLIASH_GITLAB_TOKEN")
	default:
		return nil, fmt.Errorf("gitlab: %s answered %d", project, resp.StatusCode)
	}
	var raw []struct {
		Tag        string    `json:"tag_name"`
		ReleasedAt time.Time `json:"released_at"`
		Upcoming   bool      `json:"upcoming_release"`
		Links      struct {
			Self string `json:"self"`
		} `json:"_links"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, fmt.Errorf("gitlab: %w", err)
	}
	releases := make([]GitHubRelease, 0, len(raw))
	for _, r := range raw {
		link := r.Links.Self
		if link == "" {
			link = "https://" + hostName + "/" + path + "/-/releases/" + url.PathEscape(r.Tag)
		}
		releases = append(releases, GitHubRelease{Tag: r.Tag, URL: link, PublishedAt: r.ReleasedAt, Draft: r.Upcoming})
	}
	g.mu.Lock()
	g.cache[project] = glCache{releases: releases, at: time.Now()}
	g.mu.Unlock()
	return releases, nil
}

// SetGitLab enables release dates and changelog links from GitLab releases.
func (c *Checker) SetGitLab(g *GitLab) { c.gitlab = g }
