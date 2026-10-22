// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package ui

import (
	"context"
	"encoding/csv"
	"strings"
	"testing"

	"github.com/pipozzz/goliash/internal/store"
)

// The Updates page downloads what it shows as CSV, the filter's words included.
func TestUpdatesCSV(t *testing.T) {
	e := newUIEnv(t)
	ctx := context.Background()
	v := e.as(store.RoleViewer)
	svc, _ := e.st.GetServiceByName(ctx, e.ws.Scope(), e.serviceOf("evil"))
	if _, err := e.st.InsertReleases(ctx, e.ws.Scope(), svc.ID, []string{"1.28.0"}); err != nil {
		t.Fatal(err)
	}
	_ = e.ui.checker.EvaluateDrift(ctx, e.ws.Scope())
	if _, page := get(t, v, e.srv.URL+"/updates", nil); !strings.Contains(page, `href="/updates.csv"`) {
		t.Fatal("no CSV download on Updates")
	}
	_, body := get(t, v, e.srv.URL+"/updates.csv", nil)
	rows, err := csv.NewReader(strings.NewReader(body)).ReadAll()
	if err != nil || len(rows) < 2 || rows[0][0] != "service" {
		t.Fatalf("csv %v: %q", err, body)
	}
	found := false
	for _, r := range rows[1:] {
		found = found || r[0] == svc.Name && r[6] == "1.28.0"
	}
	if !found {
		t.Errorf("the upgrade to 1.28.0 is missing: %q", body)
	}
	if _, body = get(t, v, e.srv.URL+"/updates.csv?q=no-such-thing", nil); strings.Count(body, "\n") != 1 {
		t.Errorf("the filter's words do not apply: %q", body)
	}
}

// What the release notes on the way say breaks shows on the upgrade, in the CSV and in the checklist.
func TestUpdatesBreaking(t *testing.T) {
	e := newUIEnv(t)
	ctx := context.Background()
	v := e.as(store.RoleViewer)
	svc, _ := e.st.GetServiceByName(ctx, e.ws.Scope(), e.serviceOf("evil"))
	if _, err := e.st.InsertReleases(ctx, e.ws.Scope(), svc.ID, []string{"1.28.0"}); err != nil {
		t.Fatal(err)
	}
	if err := e.st.SetReleaseBreaking(ctx, e.ws.Scope(), svc.ID, "1.28.0", "Removed the ssl directive"); err != nil {
		t.Fatal(err)
	}
	_ = e.ui.checker.EvaluateDrift(ctx, e.ws.Scope())
	_, page := get(t, v, e.srv.URL+"/updates", nil)
	for _, want := range []string{">breaking in 1.28.0</span>", "<li>1.28.0: Removed the ssl directive</li>", "breaking: 1.28.0: Removed the ssl directive"} {
		if !strings.Contains(page, want) {
			t.Errorf("Updates misses %q", want)
		}
	}
	if _, body := get(t, v, e.srv.URL+"/updates.csv?q=ssl", nil); !strings.Contains(body, "1.28.0: Removed the ssl directive") {
		t.Errorf("CSV: %q", body)
	}
}
