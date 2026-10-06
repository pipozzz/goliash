// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package ui

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/pipozzz/goliash/internal/store"
)

func TestEnvState(t *testing.T) {
	v := []VersionView{{Tag: "1.0.0"}}
	for _, c := range []struct {
		cell   MatrixCell
		latest string
		want   string
	}{
		{MatrixCell{}, "1.0.0", stNone},
		{MatrixCell{Versions: v}, "1.0.0", stCurrent},
		{MatrixCell{Versions: v}, "", stUnknown},
		{MatrixCell{Versions: v, Drifts: []DriftBadge{{Kind: "upstream"}}}, "1.1.0", stBehind},
		{MatrixCell{Versions: v, Drifts: []DriftBadge{{Kind: "upstream"}, {Kind: "eol"}}}, "2.0.0", stAttention},
	} {
		if got := envState(c.cell, c.latest); got != c.want {
			t.Errorf("%+v: %s, want %s", c.cell, got, c.want)
		}
	}
	if worse(stCurrent, stAttention) != stAttention || worse(stBehind, stNone) != stBehind {
		t.Error("worse")
	}
}

// The favicon has nine cells; a single service in need of attention still shows warm,
// in the logo's warm place (middle right).
func TestFaviconSVG(t *testing.T) {
	svg := faviconSVG(TileCounts{Current: 40, Attention: 1})
	if strings.Count(svg, "<rect") != 10 || strings.Count(svg, "#ffb347") != 1 || !strings.Contains(svg, `x="61" y="39" width="18" height="18" rx="4.5" fill="#ffb347"`) {
		t.Errorf("favicon %s", svg)
	}
	if strings.Contains(faviconSVG(TileCounts{Current: 3}), "#ffb347") {
		t.Error("warm cell with nothing to attend to")
	}
}

func TestTilesPage(t *testing.T) {
	e := newUIEnv(t)
	viewer := e.as(store.RoleViewer)
	_, body := get(t, viewer, e.srv.URL+"/tiles", nil)
	for _, want := range []string{`class="logo-board`, `data-key="app-`, `aria-label="View"`, `hx-select=".tiles-live"`, "TV mode", `href="/ui/favicon.svg"`} {
		if !strings.Contains(body, want) {
			t.Errorf("tiles miss %q", want)
		}
	}
	// The browser remembers the tiles as its home view, until it asks for the table.
	if _, body = get(t, viewer, e.srv.URL+"/", nil); !strings.Contains(body, "Service tiles") {
		t.Error("tiles are not the home view after visiting them")
	}
	if _, body = get(t, viewer, e.srv.URL+"/?view=table", nil); !strings.Contains(body, "Service × environment") {
		t.Error("view=table shows no table")
	}
	if _, body = get(t, viewer, e.srv.URL+"/", nil); !strings.Contains(body, "Service × environment") {
		t.Error("the table is not remembered")
	}

	// Drilled into an application: its services, a way back, and their details.
	_, body = get(t, viewer, e.srv.URL+"/tiles?app=other", nil)
	if !strings.Contains(body, "All applications") || !strings.Contains(body, `class="tile-panel" id="svc-`) {
		t.Errorf("drill-down: %s", body[:min(len(body), 300)])
	}
	if _, body = get(t, viewer, e.srv.URL+"/tiles?env=side", nil); !strings.Contains(body, `class="env-boards"`) {
		t.Error("no boards side by side")
	}
	if _, body = get(t, viewer, e.srv.URL+"/tiles?tv=1", nil); !strings.Contains(body, `class="kiosk"`) || strings.Contains(body, `<header class="top">`) {
		t.Error("TV mode keeps the header")
	}
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, e.srv.URL+"/ui/favicon.svg", nil)
	resp, err := viewer.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.Header.Get("Content-Type") != "image/svg+xml" || resp.StatusCode != 200 {
		t.Errorf("favicon %d %s", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
}
