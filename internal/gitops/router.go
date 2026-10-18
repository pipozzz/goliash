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
