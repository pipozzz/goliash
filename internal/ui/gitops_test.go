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
