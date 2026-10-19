// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package gitops

import (
	"context"
	"fmt"
)

// Provider opens pull or merge requests in the repositories it supports.
type Provider interface {
	Supports(repo string) bool
	Plan(ctx context.Context, b Bump) (Plan, error)
	OpenBump(ctx context.Context, b Bump) (string, error)
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
