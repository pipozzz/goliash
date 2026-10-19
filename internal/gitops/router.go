// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package gitops

import (
	"context"
	"fmt"
	"slices"
	"strings"
)

// Provider opens pull or merge requests in the repositories it supports.
type Provider interface {
	Supports(repo string) bool
	Plan(ctx context.Context, b Bump) (Plan, error)
	OpenBump(ctx context.Context, b Bump) (string, error)
	PullState(ctx context.Context, pullURL string) (string, error)
	Pinned(ctx context.Context, b Bump) ([]string, error)
	OpenPulls(ctx context.Context, repo string) ([]Pull, error)
}

// Router sends each upgrade to the provider of its repository's host: GitHub, GitHub Enterprise, GitLab.
type Router []Provider

// Supports says whether a provider takes the repository.
func (r Router) Supports(repo string) bool {
	for _, p := range r {
		if p.Supports(repo) {
			return true
		}
	}
	return false
}

// OpenBump opens the upgrade with the repository's provider.
func (r Router) OpenBump(ctx context.Context, b Bump) (string, error) {
	for _, p := range r {
		if p.Supports(b.Repo) {
			return p.OpenBump(ctx, b)
		}
	}
	return "", fmt.Errorf("no token for %s", b.Repo)
}

// Plan is what a bump would change: the branch it starts from and each file before and after.
type Plan struct {
	Base    string
	Changes []Change
}

// Change is one file a bump changes.
type Change struct {
	Path     string
	Old, New []byte
}

// Plan reads what the upgrade would change with the repository's provider, and changes nothing.
func (r Router) Plan(ctx context.Context, b Bump) (Plan, error) {
	for _, p := range r {
		if p.Supports(b.Repo) {
			return p.Plan(ctx, b)
		}
	}
	return Plan{}, fmt.Errorf("no token for %s", b.Repo)
}

// PullState asks the pull request's forge whether it is open, merged or closed.
func (r Router) PullState(ctx context.Context, pullURL string) (string, error) {
	for _, p := range r {
		if p.Supports(repoOf(pullURL)) {
			return p.PullState(ctx, pullURL)
		}
	}
	return "", fmt.Errorf("no token for %s", pullURL)
}

// repoOf is the repository address of a pull or merge request address.
func repoOf(pullURL string) string {
	for _, sep := range []string{"/-/merge_requests/", "/pull/", "/pulls/"} {
		if i := strings.Index(pullURL, sep); i > 0 {
			return pullURL[:i]
		}
	}
	return pullURL
}

// Pinned reads, through the provider of the repository's host, the tags the application's files pin its image at.
func (r Router) Pinned(ctx context.Context, b Bump) ([]string, error) {
	for _, p := range r {
		if p.Supports(b.Repo) {
			return p.Pinned(ctx, b)
		}
	}
	return nil, fmt.Errorf("no provider for %s", b.Repo)
}

// OpenPulls lists, through the provider of the repository's host, its open pull or merge requests.
func (r Router) OpenPulls(ctx context.Context, repo string) ([]Pull, error) {
	for _, p := range r {
		if p.Supports(repo) {
			return p.OpenPulls(ctx, repo)
		}
	}
	return nil, fmt.Errorf("no provider for %s", repo)
}

// appendNew appends the tags not in tags yet.
func appendNew(tags []string, more ...string) []string {
	for _, m := range more {
		if !slices.Contains(tags, m) {
			tags = append(tags, m)
		}
	}
	return tags
}
