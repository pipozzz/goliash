// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package ingest

import (
	"testing"

	"github.com/pipozzz/goliash/pkg/agentproto"
)

// A workload without a GitOps tool names its place in Git in its labels (Nomad meta, Docker labels, ECS tags).
func TestGitOpsFromLabels(t *testing.T) {
	got := gitOps([]agentproto.Workload{
		{ID: "n/vault", Name: "vault", Labels: map[string]string{
			"goliash.git.repo": "https://github.com/acme/infra", "goliash.git.path": "nomad/vault.nomad.hcl", "goliash.git.branch": "prod",
		}},
		{ID: "n/other", Name: "other", Labels: map[string]string{"app": "x"}},
	})
	if len(got) != 1 {
		t.Fatalf("got %+v", got)
	}
	g := got[0]
	if g.WorkloadID != "n/vault" || g.Tool != "git" || g.App != "vault" || g.Repo != "https://github.com/acme/infra" ||
		g.Path != "nomad/vault.nomad.hcl" || g.TargetRevision != "prod" || g.Sync != "" {
		t.Fatalf("got %+v", g)
	}
}
