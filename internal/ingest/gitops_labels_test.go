// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package ingest

import (
	"testing"
	"time"

	"github.com/pipozzz/goliash/internal/store"
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

// A version change of a workload a GitOps application deploys keeps the commit it had synced; a retag, a chart
// version and a workload without an application do not.
func TestDeployCommits(t *testing.T) {
	sc := store.Scope{OrgID: "o", WorkspaceID: "w"}
	at := time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)
	changes := store.SnapshotChanges{
		Upsert: []store.Instance{{ID: "i1", WorkloadID: "api"}, {ID: "i2", WorkloadID: "db"}, {ID: "i3", WorkloadID: "plain"}, {ID: "i4", WorkloadID: "chart"}},
		Events: []store.Event{
			{Type: "version_changed", ServiceID: "s-api", EnvironmentID: "prod", InstanceID: "i1", FromVersion: "1.1.0", ToVersion: "1.2.0", At: at},
			{Type: "version_changed", ServiceID: "s-db", EnvironmentID: "prod", InstanceID: "i2", FromVersion: "16.1", ToVersion: "16.1", Note: "retag", At: at},
			{Type: "version_changed", ServiceID: "s-plain", EnvironmentID: "prod", InstanceID: "i3", ToVersion: "2.0.0", At: at},
			{Type: "version_changed", ServiceID: "s-chart", EnvironmentID: "prod", InstanceID: "i4", ToVersion: "3.0.0", At: at},
			{Type: "deployed", ServiceID: "s-api", EnvironmentID: "prod", InstanceID: "i1", ToVersion: "1.2.0", At: at},
		},
	}
	gops := []store.WorkloadGitOps{
		{WorkloadID: "api", Repo: "https://github.com/acme/deploy", Revision: "4f1c2e9ab01"},
		{WorkloadID: "db", Repo: "https://github.com/acme/deploy", Revision: "4f1c2e9ab01"},
		{WorkloadID: "chart", Repo: "https://charts.example.com", Revision: "3.0.0"},
	}
	got := deployCommits(sc, changes, gops)
	if len(got) != 1 {
		t.Fatalf("got %+v", got)
	}
	if d := got[0]; d.Scope != sc || d.ServiceID != "s-api" || d.To != "1.2.0" || d.Revision != "4f1c2e9ab01" || d.Repo != "https://github.com/acme/deploy" || !d.DeployedAt.Equal(at) {
		t.Errorf("got %+v", d)
	}
}
