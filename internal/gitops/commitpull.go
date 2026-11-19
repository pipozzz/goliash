// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package gitops

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// MergedPull is the pull (or merge) request that merged a commit: who opened it, and when it was merged.
type MergedPull struct {
	URL, Title, By string
	MergedAt       time.Time
}

// CommitPuller finds the pull request that merged a commit: ok is false when none did (a direct push).
type CommitPuller interface {
	CommitPull(ctx context.Context, repo, sha string) (MergedPull, bool, error)
}

// CommitPull asks the provider of the repository's host which pull request merged the commit.
func (r Router) CommitPull(ctx context.Context, repo, sha string) (MergedPull, bool, error) {
	for _, p := range r {
		if p.Supports(repo) {
			if c, ok := p.(CommitPuller); ok {
				return c.CommitPull(ctx, repo, sha)
			}
			return MergedPull{}, false, nil
		}
	}
	return MergedPull{}, false, fmt.Errorf("no provider for %s", repo)
}

// CommitPull asks the restricted provider, for a repository it may be used on.
func (r restricted) CommitPull(ctx context.Context, repo, sha string) (MergedPull, bool, error) {
	c, ok := r.Provider.(CommitPuller)
	if !ok || !r.Supports(repo) {
		return MergedPull{}, false, nil
	}
	return c.CommitPull(ctx, repo, sha)
}

// CommitPull finds the merged pull request a commit belongs to (GitHub lists the open ones holding it too).
func (g *GitHub) CommitPull(ctx context.Context, repo, sha string) (MergedPull, bool, error) {
	owner, name, ok := g.ownerRepo(repo)
	if !ok {
		return MergedPull{}, false, fmt.Errorf("%s is not a GitHub repository", repo)
	}
	var list []struct {
		HTMLURL string `json:"html_url"`
		Title   string `json:"title"`
		User    struct {
			Login string `json:"login"`
		} `json:"user"`
		MergedAt *time.Time `json:"merged_at"`
	}
	if err := g.do(ctx, http.MethodGet, "/repos/"+owner+"/"+name+"/commits/"+url.PathEscape(sha)+"/pulls", nil, &list); err != nil {
		if notFound(err) {
			return MergedPull{}, false, nil
		}
		return MergedPull{}, false, err
	}
	for _, p := range list {
		if p.MergedAt != nil {
			return MergedPull{URL: p.HTMLURL, Title: p.Title, By: p.User.Login, MergedAt: p.MergedAt.UTC()}, true, nil
		}
	}
	return MergedPull{}, false, nil
}

// CommitPull finds the merged merge request a commit belongs to.
func (g *GitLab) CommitPull(ctx context.Context, repo, sha string) (MergedPull, bool, error) {
	if !g.Supports(repo) {
		return MergedPull{}, false, fmt.Errorf("%s is not on %s", repo, g.base)
	}
	u, _ := url.Parse(repo)
	project := "/projects/" + url.PathEscape(trimGit(u.Path))
	var list []struct {
		WebURL string `json:"web_url"`
		Title  string `json:"title"`
		State  string `json:"state"`
		Author struct {
			Username string `json:"username"`
		} `json:"author"`
		MergedAt *time.Time `json:"merged_at"`
	}
	if err := g.do(ctx, http.MethodGet, project+"/repository/commits/"+url.PathEscape(sha)+"/merge_requests", nil, &list); err != nil {
		if notFound(err) {
			return MergedPull{}, false, nil
		}
		return MergedPull{}, false, err
	}
	for _, m := range list {
		if m.State == "merged" {
			p := MergedPull{URL: m.WebURL, Title: m.Title, By: m.Author.Username}
			if m.MergedAt != nil {
				p.MergedAt = m.MergedAt.UTC()
			}
			return p, true, nil
		}
	}
	return MergedPull{}, false, nil
}

// CommitPull finds the pull request a commit merged with: Gitea answers for the merge commit (or the squashed one).
func (g *Gitea) CommitPull(ctx context.Context, repo, sha string) (MergedPull, bool, error) {
	owner, name, ok := g.ownerRepo(repo)
	if !ok {
		return MergedPull{}, false, fmt.Errorf("%s is not on %s", repo, g.base)
	}
	var pr struct {
		HTMLURL string `json:"html_url"`
		Title   string `json:"title"`
		User    struct {
			Login string `json:"login"`
		} `json:"user"`
		Merged   bool       `json:"merged"`
		MergedAt *time.Time `json:"merged_at"`
	}
	if err := g.do(ctx, http.MethodGet, "/repos/"+url.PathEscape(owner)+"/"+url.PathEscape(name)+"/commits/"+url.PathEscape(sha)+"/pull", nil, &pr); err != nil {
		if notFound(err) {
			return MergedPull{}, false, nil
		}
		return MergedPull{}, false, err
	}
	if !pr.Merged || pr.HTMLURL == "" {
		return MergedPull{}, false, nil
	}
	p := MergedPull{URL: pr.HTMLURL, Title: pr.Title, By: pr.User.Login}
	if pr.MergedAt != nil {
		p.MergedAt = pr.MergedAt.UTC()
	}
	return p, true, nil
}

// CommitPull makes up the demo's merged pull request: Renovate's, merged an hour before the commit was synced.
func (Demo) CommitPull(_ context.Context, _, sha string) (MergedPull, bool, error) {
	n := 300
	for _, c := range sha {
		n += int(c) % 7
	}
	return MergedPull{
		URL: fmt.Sprintf("https://github.com/acme/deploy/pull/%d", n), Title: "Update image tags", By: "renovate[bot]",
	}, true, nil
}

// trimGit is a repository's path without slashes around it and without .git: acme/deploy.
func trimGit(p string) string {
	return strings.TrimSuffix(strings.Trim(p, "/"), ".git")
}
