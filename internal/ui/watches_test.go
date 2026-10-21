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

// A member watches nginx after an advance notice: the Security page shows what runs it and why, Updates marks its
// upgrades, a viewer may look but not watch, and the watch can be ended.
func TestSecurityWatches(t *testing.T) {
	e := newUIEnv(t)
	ctx := context.Background()
	m, viewer := e.as(store.RoleMember), e.as(store.RoleViewer)
	if code, _, _ := post(t, viewer, e.srv.URL+"/security/watches", url.Values{"term": {"nginx"}}); code != 403 && code != 404 {
		t.Fatalf("a viewer started a watch: %d", code)
	}
	post(t, m, e.srv.URL+"/security/watches", url.Values{"term": {"nginx"}, "note": {"two critical fixes on Oct 21"}, "days": {"7"}})
	ws, _ := e.st.ListWatches(ctx, e.ws.Scope(), time.Now())
	if len(ws) != 1 || ws[0].Term != "nginx" || ws[0].Until.IsZero() {
		t.Fatalf("watches %+v", ws)
	}
	_, page := get(t, m, e.srv.URL+"/security", nil)
	for _, want := range []string{`id="watches"`, "“nginx”", "two critical fixes on Oct 21", "runs docker.io/library/nginx", "Stop watching"} {
		if !strings.Contains(page, want) {
			t.Errorf("Security page misses %q", want)
		}
	}
	if _, page = get(t, viewer, e.srv.URL+"/security", nil); !strings.Contains(page, "“nginx”") || strings.Contains(page, "Stop watching") {
		t.Error("a viewer does not see the watch, or may end it")
	}
	svc, _ := e.st.GetServiceByName(ctx, e.ws.Scope(), e.serviceOf("evil"))
	if _, err := e.st.InsertReleases(ctx, e.ws.Scope(), svc.ID, []string{"1.28.0"}); err != nil {
		t.Fatal(err)
	}
	_ = e.ui.checker.EvaluateDrift(ctx, e.ws.Scope())
	if _, page = get(t, m, e.srv.URL+"/updates", nil); !strings.Contains(page, ">watch: nginx</a>") {
		t.Error("Updates does not mark the watched upgrade")
	}
	post(t, m, e.srv.URL+"/security/watches/"+ws[0].ID+"/delete", nil)
	if ws, _ = e.st.ListWatches(ctx, e.ws.Scope(), time.Now()); len(ws) != 0 {
		t.Fatal("the watch did not end")
	}
}
