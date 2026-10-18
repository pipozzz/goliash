// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package versions

import (
	"testing"
	"time"

	"github.com/pipozzz/goliash/internal/store"
)

func TestGitOpsLinks(t *testing.T) {
	for repo, want := range map[string]string{
		"git@github.com:acme/deploy.git":            "https://github.com/acme/deploy",
		"https://gitlab.example.com/ops/deploy.git": "https://gitlab.example.com/ops/deploy",
		"https://github.com/acme/deploy":            "https://github.com/acme/deploy",
		"registry-1.docker.io/bitnamicharts":        "",
		"https://charts.bitnami.com":                "",
		"oci://ghcr.io/acme/charts":                 "",
	} {
		if got := RepoWebURL(repo); got != want {
			t.Errorf("RepoWebURL(%q) = %q, want %q", repo, got, want)
		}
	}
	if got := CommitWebURL("https://gitlab.example.com/ops/deploy", "abc1234"); got != "https://gitlab.example.com/ops/deploy/-/commit/abc1234" {
		t.Errorf("gitlab commit %q", got)
	}
	if got := CommitWebURL("https://github.com/acme/deploy", "15.5.0"); got != "" {
		t.Errorf("a chart version linked as a commit: %q", got)
	}
	if got := ArgoAppURL("https://argo.example.com/", "argocd", "shop prod"); got != "https://argo.example.com/applications/argocd/shop%20prod" {
		t.Errorf("argo url %q", got)
	}
	if ArgoAppURL("", "argocd", "x") != "" {
		t.Error("argo url without a base")
	}
}

func TestGitOpsTrouble(t *testing.T) {
	now := time.Now()
	app := func(sync, health string, age time.Duration) GitOpsApp {
		return GitOpsApp{WorkloadGitOps: store.WorkloadGitOps{Sync: sync, Health: health, SyncSince: now.Add(-age)}}
	}
	for _, c := range []struct {
		a    GitOpsApp
		want string
	}{
		{app("OutOfSync", "Healthy", time.Hour), "out of sync"},
		{app("OutOfSync", "Progressing", 2*time.Minute), ""}, // a sync under way
		{app("Synced", "Degraded", time.Hour), "degraded"},
		{app("Synced", "Missing", time.Hour), "missing"},
		{app("Synced", "Healthy", 48*time.Hour), ""},
	} {
		if got := c.a.Trouble(now); got != c.want {
			t.Errorf("%s/%s: %q, want %q", c.a.Sync, c.a.Health, got, c.want)
		}
	}
}
