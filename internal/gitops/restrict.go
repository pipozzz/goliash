// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package gitops

import "strings"

// Restrict keeps the router's providers to repositories that start with one of prefixes (github.com/acme/deploy,
// gitlab.example.com/platform/): the server's token is used on what agents report, and an agent's workspace is
// not necessarily trusted with every repository the token reaches. No prefixes: no restriction.
func Restrict(r Router, prefixes []string) Router {
	var keep []string
	for _, p := range prefixes {
		if p = normalizeRepo(p); p != "" {
			keep = append(keep, p)
		}
	}
	if len(keep) == 0 {
		return r
	}
	out := make(Router, len(r))
	for i, p := range r {
		out[i] = restricted{Provider: p, prefixes: keep}
	}
	return out
}

type restricted struct {
	Provider
	prefixes []string
}

func (r restricted) Supports(repo string) bool {
	n := normalizeRepo(repo)
	for _, p := range r.prefixes {
		if strings.HasPrefix(n, p) {
			return r.Provider.Supports(repo)
		}
	}
	return false
}

// normalizeRepo writes a repository as host/path: https://github.com/acme/deploy.git is github.com/acme/deploy.
func normalizeRepo(repo string) string {
	repo = strings.ToLower(strings.TrimSpace(repo))
	for _, p := range []string{"https://", "http://", "ssh://", "git@"} {
		repo = strings.TrimPrefix(repo, p)
	}
	repo = strings.Replace(repo, ":", "/", 1) // git@github.com:acme/deploy
	return strings.TrimSuffix(repo, ".git")
}
