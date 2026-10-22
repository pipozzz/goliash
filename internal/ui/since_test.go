// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package ui

import (
	"context"
	"strings"
	"testing"

	"github.com/pipozzz/goliash/internal/store"
)

// The Security page opens with the security brief: nothing new, then a release that fixes vulnerabilities in
// what runs, linked to the service.
func TestSinceYesterday(t *testing.T) {
	e := newUIEnv(t)
	ctx := context.Background()
	v := e.as(store.RoleViewer)
	if _, page := get(t, v, e.srv.URL+"/security", nil); !strings.Contains(page, `id="since-yesterday"`) || !strings.Contains(page, "Nothing new to look at") {
		t.Fatal("no Since yesterday panel, or it is not empty")
	}
	svc, _ := e.st.GetServiceByName(ctx, e.ws.Scope(), e.serviceOf("evil"))
	if _, err := e.st.InsertReleases(ctx, e.ws.Scope(), svc.ID, []string{"1.28.0"}); err != nil {
		t.Fatal(err)
	}
	if err := e.st.SetReleaseSecurity(ctx, e.ws.Scope(), svc.ID, "1.28.0", "CVE-2026-1"); err != nil {
		t.Fatal(err)
	}
	_ = e.ui.checker.EvaluateDrift(ctx, e.ws.Scope())
	_, page := get(t, v, e.srv.URL+"/security", nil)
	for _, want := range []string{">security release</span>", "1.28.0 fixes vulnerabilities; 1.27.2 runs", `<a class="u" href="/updates">1 upgrade fixing vulnerabilities</a>`} {
		if !strings.Contains(page, want) {
			t.Errorf("Since yesterday misses %q", want)
		}
	}
	if strings.Contains(page, "Nothing new to look at") {
		t.Error("still says nothing is new")
	}
}
