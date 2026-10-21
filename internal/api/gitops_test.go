// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/pipozzz/goliash/internal/store"
)

// A degraded application says the version change just before it; the pull requests Goliash opened are listed with
// whether their version runs.
func TestGitOpsAfterChangeAndPullRequests(t *testing.T) {
	e := newPublicEnv(t)
	ctx := context.Background()
	ts, _ := e.st.ListTargets(ctx, e.sc)
	var prod store.Target
	for _, x := range ts {
		if x.Name == "k8s-prod" {
			prod = x
		}
	}
	web, _ := e.st.GetServiceByName(ctx, e.sc, "web")
	g := store.WorkloadGitOps{
		WorkloadID: "w", Tool: "argocd", App: "web-prod", Repo: "https://github.com/acme/deploy.git",
		Path: "apps/web/prod", TargetRevision: "main", Sync: "Synced", Health: "Degraded", ManualSync: true,
	}
	if err := e.st.SetWorkloadGitOps(ctx, e.sc, prod.ID, []store.WorkloadGitOps{g}, true, time.Now().Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := e.st.InsertEvent(ctx, e.sc, store.Event{
		Type: "version_changed", ServiceID: web.ID, EnvironmentID: prod.EnvironmentID, TargetID: prod.ID,
		FromVersion: "1.27.1", ToVersion: "1.27.2", At: time.Now().Add(-2 * time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	code, body := e.call(nil, "GET", "/api/v1/gitops", e.token, "")
	if code != 200 || !strings.Contains(body, `"trouble":"degraded","version":"1.27.2","after_change":{"from":"1.27.1","to":"1.27.2"`) {
		t.Fatalf("%d %s", code, body)
	}
	if err := e.st.RecordGitOpsPull(ctx, store.GitOpsPull{
		Scope: e.sc, ServiceID: web.ID, Environment: "prod", TargetID: prod.ID, WorkloadID: "w",
		From: "1.27.1", To: "1.27.2", URL: "https://github.com/acme/deploy/pull/3", OpenedBy: "auto-pr",
	}); err != nil {
		t.Fatal(err)
	}
	code, body = e.call(nil, "GET", "/api/v1/pull-requests", e.token, "")
	if code != 200 || !strings.Contains(body, `"service":"web","environment":"prod","target":"k8s-prod","from":"1.27.1","to":"1.27.2"`) ||
		!strings.Contains(body, `"state":"open","opened_by":"auto-pr"`) || !strings.Contains(body, `"running":true`) {
		t.Fatalf("%d %s", code, body)
	}
	// Merged, and 1.28.0 does not run: when it was merged, and why it waits.
	if err := e.st.RecordGitOpsPull(ctx, store.GitOpsPull{
		Scope: e.sc, ServiceID: web.ID, Environment: "prod", TargetID: prod.ID, WorkloadID: "w",
		From: "1.27.2", To: "1.28.0", URL: "https://github.com/acme/deploy/pull/4", OpenedBy: "alice@example.com",
	}); err != nil {
		t.Fatal(err)
	}
	pulls, _ := e.st.ListGitOpsPulls(ctx, e.sc)
	merged := time.Date(2026, 10, 20, 9, 15, 0, 0, time.UTC)
	for _, p := range pulls {
		if p.To == "1.28.0" {
			_ = e.st.SetGitOpsPullState(ctx, e.sc, p.ID, "merged", merged)
		}
	}
	_, body = e.call(nil, "GET", "/api/v1/pull-requests", e.token, "")
	if !strings.Contains(body, `"merged_at":"2026-10-20T09:15:00Z","running":false,"waiting":"Argo CD syncs it by hand"`) {
		t.Fatalf("merged, not deployed: %s", body)
	}
}

// The posture says why a production service's newest release is unknown.
func TestPostureUnknownWhy(t *testing.T) {
	e := newPublicEnv(t)
	ctx := context.Background()
	web, _ := e.st.GetServiceByName(ctx, e.sc, "web")
	web.VersionPolicy = json.RawMessage(`{"tag_filter":"^never$"}`) // no release matches
	if err := e.st.UpdateService(ctx, web); err != nil {
		t.Fatal(err)
	}
	if err := e.st.RecordUpstreamCheck(ctx, e.sc, web.ID, ""); err != nil {
		t.Fatal(err)
	}
	code, body := e.call(nil, "GET", "/api/v1/posture", e.token, "")
	if code != 200 || !strings.Contains(body, `"unknown_upstream_why":[{"service":"web","repository":"docker.io/library/nginx","short":"no comparable tag"`) {
		t.Fatalf("%d %s", code, body)
	}
}
