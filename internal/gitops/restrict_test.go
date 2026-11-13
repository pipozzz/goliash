// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package gitops

import (
	"context"
	"testing"
)

type anyRepo struct{ Provider }

func (anyRepo) Supports(string) bool { return true }

func (anyRepo) OpenBump(context.Context, Bump) (string, error) { return "opened", nil }

func TestRestrict(t *testing.T) {
	r := Restrict(Router{anyRepo{}}, []string{"github.com/acme/deploy", "https://gitlab.example.com/platform/"})
	for repo, want := range map[string]bool{
		"https://github.com/acme/deploy.git":            true,
		"git@github.com:acme/deploy.git":                true,
		"https://gitlab.example.com/platform/infra.git": true,
		"https://github.com/other/secret.git":           false,
	} {
		if got := r.Supports(repo); got != want {
			t.Errorf("%s: %v", repo, got)
		}
	}
	if _, err := r.OpenBump(context.Background(), Bump{Repo: "https://github.com/other/secret.git"}); err == nil {
		t.Error("a pull request opened outside the allowed repositories")
	}
	if !Restrict(Router{anyRepo{}}, nil).Supports("https://github.com/other/secret.git") {
		t.Error("no prefixes restrict")
	}
}
