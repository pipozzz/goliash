// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package ui

import (
	"context"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/pipozzz/goliash/internal/store"
)

// A new version on the service page links the pull request that brought it, Renovate's as well as Goliash's.
func TestTimelinePulls(t *testing.T) {
	e := newUIEnv(t)
	ctx := context.Background()
	sc := e.ws.Scope()
	envs, _ := e.st.ListEnvironments(ctx, sc)
	prod := envs[len(envs)-1].ID
	api, _ := e.st.EnsureService(ctx, sc, "api")
	at := time.Now().Add(-time.Hour)
	for _, ev := range []store.Event{
		{ID: store.NewID(), Type: "version_changed", ServiceID: api.ID, EnvironmentID: prod, FromVersion: "1.1.0", ToVersion: "1.2.0", At: at},
		{ID: store.NewID(), Type: "version_changed", ServiceID: api.ID, EnvironmentID: prod, FromVersion: "1.0.0", ToVersion: "1.1.0", At: at.Add(-48 * time.Hour)},
	} {
		if err := e.st.InsertEvent(ctx, sc, ev); err != nil {
			t.Fatal(err)
		}
	}
	if err := e.st.RecordDeployCommit(ctx, store.DeployCommit{Scope: sc, ServiceID: api.ID, EnvironmentID: prod, To: "1.2.0", Repo: "https://github.com/acme/deploy", Revision: "abc1234", DeployedAt: at}); err != nil {
		t.Fatal(err)
	}
	pending, _ := e.st.PendingDeployCommits(ctx, at.Add(-time.Hour), 10)
	if len(pending) != 1 {
		t.Fatalf("pending %+v", pending)
	}
	if err := e.st.SetDeployCommitPull(ctx, sc, pending[0].ID, "https://github.com/acme/deploy/pull/412", "Update api", "renovate[bot]", at.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	_, page := get(t, e.as(store.RoleViewer), e.srv.URL+"/services/"+url.PathEscape("api"), nil)
	if !strings.Contains(page, `href="https://github.com/acme/deploy/pull/412"`) || !strings.Contains(page, "PR #412 · renovate[bot]") {
		t.Error("the new version does not link the pull request that brought it")
	}
	if strings.Count(page, "/pull/412") != 1 {
		t.Error("the older version, which came another way, links a pull request too")
	}
}

func TestPullLabelOf(t *testing.T) {
	for p, want := range map[eventPull]string{
		{"https://github.com/acme/deploy/pull/412", "renovate[bot]"}:   "PR #412 · renovate[bot]",
		{"https://gitlab.com/acme/deploy/-/merge_requests/3", "alice"}: "PR #3 · alice",
		{"https://example.com/review", ""}:                             "pull request",
	} {
		if got := pullLabelOf(p); got != want {
			t.Errorf("pullLabelOf(%+v) = %q, want %q", p, got, want)
		}
	}
}
