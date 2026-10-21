// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package ui

import (
	"context"
	"html"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/pipozzz/goliash/internal/store"
)

// One search finds a service by the image it runs, its history and the pull requests Goliash opened for it.
func TestSearch(t *testing.T) {
	e := newUIEnv(t)
	ctx := context.Background()
	sc := e.ws.Scope()
	name := e.serviceOf("evil")
	svc, _ := e.st.GetServiceByName(ctx, sc, name)
	if err := e.st.RecordGitOpsPull(ctx, store.GitOpsPull{
		Scope: sc, ServiceID: svc.ID, Environment: "prod", TargetID: e.tgt.ID, WorkloadID: "w1",
		From: "1.27.2", To: "1.28.0", URL: "https://github.com/acme/deploy/pull/7", OpenedBy: "auto-pr",
	}); err != nil {
		t.Fatal(err)
	}
	if err := e.st.InsertEvent(ctx, sc, store.Event{Type: "version_changed", ServiceID: svc.ID, FromVersion: "1.27.1", ToVersion: "1.27.2", At: time.Now().Add(-time.Hour)}); err != nil {
		t.Fatal(err)
	}
	v := e.as(store.RoleViewer)
	_, page := get(t, v, e.srv.URL+"/search?q="+url.QueryEscape("nginx"), nil)
	for _, want := range []string{"<h2>Services <span class=\"count\">1</span>", ">" + html.EscapeString(name) + "</strong>", "runs docker.io/library/nginx"} {
		if !strings.Contains(page, want) {
			t.Errorf("nginx: missing %q", want)
		}
	}
	_, page = get(t, v, e.srv.URL+"/search?q=1.28.0", nil)
	if !strings.Contains(page, "Pull requests") || !strings.Contains(page, "https://github.com/acme/deploy/pull/7") {
		t.Error("the pull request is not found")
	}
	_, page = get(t, v, e.srv.URL+"/search?q="+url.QueryEscape(name+" 1.27.1"), nil)
	if !strings.Contains(page, "History (90 days)") {
		t.Error("the version change is not found")
	}
	if _, page = get(t, v, e.srv.URL+"/search?q=zzz-nothing", nil); !strings.Contains(page, "Nothing matches") {
		t.Error("no empty state")
	}
}
