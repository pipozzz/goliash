// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"context"
	"testing"
	"time"
)

// A deploy's commit is kept once, waits to be looked up, and then carries its pull request.
func TestDeployCommits(t *testing.T) {
	forEachDialect(t, func(t *testing.T, st *Store) { testDeployCommits(t, st) })
}

func testDeployCommits(t *testing.T, st *Store) {
	ctx := context.Background()
	ws, _ := st.EnsureDefaultWorkspace(ctx)
	sc := ws.Scope()
	at := time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)
	d := DeployCommit{Scope: sc, ServiceID: "svc", EnvironmentID: "prod", To: "1.2.0", Repo: "https://github.com/acme/deploy", Revision: "abc1234", DeployedAt: at}
	for range 2 {
		if err := st.RecordDeployCommit(ctx, d); err != nil {
			t.Fatal(err)
		}
	}
	pending, err := st.PendingDeployCommits(ctx, at.Add(-time.Hour), 10)
	if err != nil || len(pending) != 1 || pending[0].Scope != sc || pending[0].Revision != "abc1234" {
		t.Fatalf("pending %+v, %v", pending, err)
	}
	if late, _ := st.PendingDeployCommits(ctx, at.Add(time.Hour), 10); len(late) != 0 {
		t.Error("a deploy before since is pending")
	}
	merged := at.Add(-30 * time.Minute)
	if err := st.SetDeployCommitPull(ctx, sc, pending[0].ID, "https://github.com/acme/deploy/pull/9", "Update api to 1.2.0", "renovate[bot]", merged); err != nil {
		t.Fatal(err)
	}
	if again, _ := st.PendingDeployCommits(ctx, at.Add(-time.Hour), 10); len(again) != 0 {
		t.Error("a looked-up commit is still pending")
	}
	list, err := st.ListDeployCommits(ctx, sc, at.Add(-24*time.Hour))
	if err != nil || len(list) != 1 || list[0].PullBy != "renovate[bot]" || !list[0].PullMergedAt.Equal(merged) || !list[0].LookedUp {
		t.Fatalf("list %+v, %v", list, err)
	}
}
