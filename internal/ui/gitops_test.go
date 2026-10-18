// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package ui

import (
	"context"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/pipozzz/goliash/internal/gitops"
	"github.com/pipozzz/goliash/internal/store"
	"github.com/pipozzz/goliash/internal/versions"
)

// A workload deployed by an Argo CD application: the service page says where from and which commit; out of
// sync for a while, the matrix cell says so.
func TestArgoCDOnServiceAndMatrix(t *testing.T) {
	e := newUIEnv(t)
	ctx := context.Background()
	sc := e.ws.Scope()
	e.ui.argoCDURL = "https://argo.example.com/"
	g := store.WorkloadGitOps{
		WorkloadID: "w1", Tool: "argocd", App: "evil-prod", AppNamespace: "argocd", Project: "shop",
		Repo: "git@github.com:acme/deploy.git", Path: "apps/evil/prod", TargetRevision: "main",
		Revision: "4f1c2e9d0b7a6c5e4f3a2b1c0d9e8f7a6b5c4d3e", Sync: "OutOfSync", Health: "Healthy",
	}
	if err := e.st.SetWorkloadGitOps(ctx, sc, e.tgt.ID, []store.WorkloadGitOps{g}, true, time.Now().Add(-2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	viewer := e.as(store.RoleViewer)
	svc := e.serviceOf("evil")
	_, page := get(t, viewer, e.srv.URL+"/services/"+url.PathEscape(svc), nil)
	for _, want := range []string{
		`<h2>Argo CD</h2>`, `href="https://argo.example.com/applications/argocd/evil-prod"`,
		`href="https://github.com/acme/deploy"`, `github.com/acme/deploy</a>`, `apps/evil/prod`, ` @ main`,
		`href="https://github.com/acme/deploy/commit/4f1c2e9d0b7a6c5e4f3a2b1c0d9e8f7a6b5c4d3e"`, `>4f1c2e9</a>`,
		`<span class="badge warn">OutOfSync</span>`, `<span class="badge ok">Healthy</span>`,
	} {
		if !strings.Contains(page, want) {
			t.Errorf("service page misses %q", want)
		}
	}
	_, matrix := get(t, viewer, e.srv.URL+"/?view=table", nil)
	if !strings.Contains(matrix, "Argo: out of sync") || !strings.Contains(matrix, "evil-prod on k8s-prod: OutOfSync, Healthy for 2h") {
		t.Errorf("matrix has no Argo CD badge")
	}

	// Back in sync: no badge, and the state's age starts again.
	g.Sync = "Synced"
	if err := e.st.SetWorkloadGitOps(ctx, sc, e.tgt.ID, []store.WorkloadGitOps{g}, true, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, matrix = get(t, viewer, e.srv.URL+"/?view=table", nil); strings.Contains(matrix, "Argo: ") {
		t.Error("an application in sync is badged")
	}
}

type fakePulls struct{ got []gitops.Bump }

func (f *fakePulls) OpenBump(_ context.Context, b gitops.Bump) (string, error) {
	f.got = append(f.got, b)
	return "https://github.com/acme/deploy/pull/7", nil
}

// With a token, a member opens the upgrade to the newest acceptable release as a pull request in the repository the
// Argo CD application deploys from.
func TestOpenGitOpsPR(t *testing.T) {
	e := newUIEnv(t)
	ctx := context.Background()
	sc := e.ws.Scope()
	pulls := &fakePulls{}
	e.ui.pulls = pulls
	g := store.WorkloadGitOps{
		WorkloadID: "w1", Tool: "argocd", App: "evil-prod", Repo: "https://github.com/acme/deploy.git",
		Path: "apps/evil/prod", TargetRevision: "main", Revision: "abc1234", Sync: "Synced", Health: "Healthy",
	}
	if err := e.st.SetWorkloadGitOps(ctx, sc, e.tgt.ID, []store.WorkloadGitOps{g}, true, time.Now()); err != nil {
		t.Fatal(err)
	}
	svc := e.serviceOf("evil")
	o, _ := versions.LoadOverview(ctx, e.st, sc)
	apps, _ := versions.GitOpsApps(ctx, e.st, sc, o)
	if len(apps) != 1 {
		t.Fatalf("apps %+v", apps)
	}
	if e.ui.pullRequestTo(apps[0], "") != "" || e.ui.pullRequestTo(apps[0], apps[0].Tag) != "" {
		t.Fatal("offered without a newer release")
	}
	newer := "999.0.0"
	if got := e.ui.pullRequestTo(apps[0], newer); got != newer {
		t.Fatalf("pullRequestTo = %q", got)
	}
	// The form only accepts what the page would offer: here nothing newer is known upstream.
	member := e.as(store.RoleMember)
	_, body, _ := post(t, member, e.srv.URL+"/services/"+url.PathEscape(svc)+"/gitops/pr",
		url.Values{"target_id": {e.tgt.ID}, "workload_id": {"w1"}, "to": {newer}})
	if !strings.Contains(body, "no longer offered") || len(pulls.got) != 0 {
		t.Fatalf("an upgrade not offered was opened: %v", pulls.got)
	}

	// A newer release upstream: the page offers it, and the pull request goes to the application's repository.
	svcRec, _ := e.st.GetServiceByName(ctx, sc, svc)
	if _, err := e.st.InsertReleases(ctx, sc, svcRec.ID, []string{newer}); err != nil {
		t.Fatal(err)
	}
	if _, page := get(t, member, e.srv.URL+"/services/"+url.PathEscape(svc), nil); !strings.Contains(page, "Open PR → "+newer) {
		t.Fatal("no Open PR button")
	}
	if _, page := get(t, e.as(store.RoleViewer), e.srv.URL+"/services/"+url.PathEscape(svc), nil); strings.Contains(page, "Open PR") {
		t.Fatal("a viewer is offered a pull request")
	}
	_, body, _ = post(t, member, e.srv.URL+"/services/"+url.PathEscape(svc)+"/gitops/pr",
		url.Values{"target_id": {e.tgt.ID}, "workload_id": {"w1"}, "to": {newer}})
	if !strings.Contains(body, "Pull request opened: https://github.com/acme/deploy/pull/7") || len(pulls.got) != 1 {
		t.Fatalf("not opened: %d", len(pulls.got))
	}
	b := pulls.got[0]
	if b.Repo != "https://github.com/acme/deploy" || b.Branch != "main" || b.Path != "apps/evil/prod" || b.From != apps[0].Tag ||
		b.To != newer || !strings.HasPrefix(b.Head, "goliash/") || !strings.Contains(b.Body, "evil-prod") {
		t.Fatalf("bump %+v", b)
	}
}

// A version waiting in staging is promoted to prod as a pull request in the repository prod's Argo CD
// application deploys from.
func TestPromoteAsPR(t *testing.T) {
	e := newUIEnv(t)
	ctx := context.Background()
	sc := e.ws.Scope()
	pulls := &fakePulls{}
	e.ui.pulls = pulls
	svc := e.serviceOf("evil")
	rec, _ := e.st.GetServiceByName(ctx, sc, svc)
	staging, _ := e.st.CreateEnvironment(ctx, sc, "staging", 20)
	stg, _ := e.st.CreateTarget(ctx, store.Target{Scope: sc, EnvironmentID: staging.ID, Platform: "kubernetes", Name: "k8s-staging"})
	if err := e.st.ApplySnapshot(ctx, store.SnapshotChanges{
		Scope: sc, SnapshotID: store.NewID(), TargetID: stg.ID, At: time.Now().Add(-48 * time.Hour),
		Upsert: []store.Instance{{
			TargetID: stg.ID, EnvironmentID: staging.ID, ServiceID: rec.ID, WorkloadID: "s1", WorkloadKind: "deployment",
			WorkloadName: "evil", ContainerName: "app", Image: "nginx:1.28.0", Tag: "1.28.0", Running: 1, IsMain: true,
		}},
	}); err != nil {
		t.Fatal(err)
	}
	g := store.WorkloadGitOps{
		WorkloadID: "w1", Tool: "argocd", App: "evil-prod", Repo: "https://github.com/acme/deploy.git",
		Path: "apps/evil/prod", TargetRevision: "main", Sync: "Synced", Health: "Healthy",
	}
	if err := e.st.SetWorkloadGitOps(ctx, sc, e.tgt.ID, []store.WorkloadGitOps{g}, true, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := e.ui.checker.EvaluateDrift(ctx, sc); err != nil { // promotions come from "behind previous env" drift
		t.Fatal(err)
	}
	member := e.as(store.RoleMember)
	if _, page := get(t, member, e.srv.URL+"/delivery", nil); !strings.Contains(page, "Open PR → prod") {
		t.Fatal("no promote button on the Delivery page")
	}
	_, body, _ := post(t, member, e.srv.URL+"/delivery/promote", url.Values{
		"service": {svc}, "env": {"prod"}, "version": {"1.28.0"},
		"target_id": {e.tgt.ID}, "workload_id": {"w1"},
	})
	if !strings.Contains(body, "Pull request opened") || len(pulls.got) != 1 {
		t.Fatalf("not opened (%d)", len(pulls.got))
	}
	if b := pulls.got[0]; b.To != "1.28.0" || b.Path != "apps/evil/prod" || !strings.Contains(b.Body, "it has run in staging since") {
		t.Fatalf("bump %+v", b)
	}
	// A promotion that does not wait is refused.
	_, body, _ = post(t, member, e.srv.URL+"/delivery/promote", url.Values{
		"service": {svc}, "env": {"prod"}, "version": {"1000.0.0"},
		"target_id": {e.tgt.ID}, "workload_id": {"w1"},
	})
	if !strings.Contains(body, "no longer waiting") || len(pulls.got) != 1 {
		t.Fatal("a promotion not waiting was opened")
	}
}
