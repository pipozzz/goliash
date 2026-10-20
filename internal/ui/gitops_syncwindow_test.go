// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package ui

import (
	"context"
	"encoding/json"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/pipozzz/goliash/internal/store"
)

// While Argo CD's sync windows keep an application from syncing, the page says so, the version policy waits,
// and a pull request opened by hand says when it will deploy. Once the window has closed the policy opens it.
func TestSyncWindowHoldsPullRequests(t *testing.T) {
	e := newUIEnv(t)
	ctx := context.Background()
	sc := e.ws.Scope()
	pulls := &fakePulls{}
	e.ui.pulls = pulls
	until := time.Now().Add(2 * time.Hour).Truncate(time.Second)
	g := store.WorkloadGitOps{
		WorkloadID: "w1", Tool: "argocd", App: "evil-prod", Repo: "https://github.com/acme/deploy.git",
		Path: "apps/evil/prod", TargetRevision: "main", Sync: "Synced", Health: "Healthy",
		SyncBlocked: `deny window "0 22 * * 5" for 56h`, SyncBlockedUntil: until,
	}
	if err := e.st.SetWorkloadGitOps(ctx, sc, e.tgt.ID, []store.WorkloadGitOps{g}, true, time.Now()); err != nil {
		t.Fatal(err)
	}
	name := e.serviceOf("evil")
	svc, _ := e.st.GetServiceByName(ctx, sc, name)
	if _, err := e.st.InsertReleases(ctx, sc, svc.ID, []string{"1.28.0"}); err != nil {
		t.Fatal(err)
	}
	svc.VersionPolicy = json.RawMessage(`{"auto_pr":{"prod":"minor"}}`)
	if err := e.st.UpdateService(ctx, svc); err != nil {
		t.Fatal(err)
	}
	m := e.as(store.RoleMember)
	if _, page := get(t, m, e.srv.URL+"/services/"+url.PathEscape(name), nil); !strings.Contains(page, `sync-window" title="Argo CD&#39;s sync windows keep evil-prod from syncing`) {
		t.Fatal("no sync window badge")
	}
	if n, _ := e.ui.AutoPRs(ctx); n != 0 {
		t.Fatal("the policy opened a pull request inside a sync window")
	}
	_, preview := get(t, m, e.srv.URL+"/services/"+url.PathEscape(name)+"/gitops/preview?"+previewQuery(e.tgt.ID, "w1", "1.28.0", ""), nil)
	if !strings.Contains(preview, "Merged, the change deploys only once it syncs again") || !strings.Contains(preview, "until "+until.UTC().Format("Mon 2 Jan 15:04")) {
		t.Fatal("the preview does not warn of the sync window")
	}

	g.SyncBlockedUntil = time.Now().Add(-time.Minute) // the window closed since the snapshot
	if err := e.st.SetWorkloadGitOps(ctx, sc, e.tgt.ID, []store.WorkloadGitOps{g}, true, time.Now()); err != nil {
		t.Fatal(err)
	}
	if n, _ := e.ui.AutoPRs(ctx); n != 1 || strings.Contains(pulls.got[0].Body, "sync window") {
		t.Fatalf("after the window: opened %d %+v", n, pulls.got)
	}
}

// An application Argo CD syncs only by hand is badged, and its pull request says merging is not deploying.
func TestManualSync(t *testing.T) {
	e := newUIEnv(t)
	ctx := context.Background()
	sc := e.ws.Scope()
	pulls := &fakePulls{}
	e.ui.pulls = pulls
	e.ui.argoCDURL = "https://argo.example.com/"
	g := store.WorkloadGitOps{
		WorkloadID: "w1", Tool: "argocd", App: "evil-prod", AppNamespace: "argocd", Repo: "https://github.com/acme/deploy.git",
		Path: "apps/evil/prod", TargetRevision: "main", Sync: "Synced", Health: "Healthy", ManualSync: true,
	}
	if err := e.st.SetWorkloadGitOps(ctx, sc, e.tgt.ID, []store.WorkloadGitOps{g}, true, time.Now()); err != nil {
		t.Fatal(err)
	}
	name := e.serviceOf("evil")
	svc, _ := e.st.GetServiceByName(ctx, sc, name)
	if _, err := e.st.InsertReleases(ctx, sc, svc.ID, []string{"1.28.0"}); err != nil {
		t.Fatal(err)
	}
	m := e.as(store.RoleMember)
	if _, page := get(t, m, e.srv.URL+"/services/"+url.PathEscape(name), nil); !strings.Contains(page, ">manual sync</span>") {
		t.Fatal("no manual sync badge")
	}
	_, preview := get(t, m, e.srv.URL+"/services/"+url.PathEscape(name)+"/gitops/preview?"+previewQuery(e.tgt.ID, "w1", "1.28.0", ""), nil)
	if !strings.Contains(preview, "Argo CD syncs this application by hand") {
		t.Fatal("the preview does not warn of the manual sync")
	}
	svc.VersionPolicy = json.RawMessage(`{"auto_pr":{"prod":"minor"}}`)
	if err := e.st.UpdateService(ctx, svc); err != nil {
		t.Fatal(err)
	}
	if n, _ := e.ui.AutoPRs(ctx); n != 1 || !strings.Contains(pulls.got[0].Body, "Argo CD syncs evil-prod by hand (no automated sync policy): once merged, sync it in [Argo CD](https://argo.example.com/applications/argocd/evil-prod)") {
		t.Fatalf("opened %d %+v", n, pulls.got)
	}
}

// A suspended Flux application says so in its own words: resumed, not a window.
func TestSyncWindowTextFlux(t *testing.T) {
	a := store.WorkloadGitOps{Tool: "flux", App: "payments", SyncBlocked: "suspended in Flux (spec.suspend): it applies nothing until resumed"}
	now := time.Now()
	if s := syncWindowText(a, now); s != "payments is suspended in Flux (spec.suspend): it applies nothing until resumed" {
		t.Fatalf("text %q", s)
	}
	if n := syncWindowNote(a, now); !strings.Contains(n, "deploys once it is resumed") {
		t.Fatalf("note %q", n)
	}
}
