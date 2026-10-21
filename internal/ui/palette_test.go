// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package ui

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/pipozzz/goliash/internal/store"
)

func TestPalette(t *testing.T) {
	e := newUIEnv(t)
	viewer := e.as(store.RoleViewer)
	if _, body := get(t, viewer, e.srv.URL+"/", nil); !strings.Contains(body, `data-palette`) {
		t.Error("no search button")
	}
	code, body := get(t, viewer, e.srv.URL+"/ui/palette.json", nil)
	var entries []PaletteEntry
	if err := json.Unmarshal([]byte(body), &entries); code != 200 || err != nil {
		t.Fatalf("%d %v %s", code, err, body)
	}
	kinds := map[string]int{}
	for _, en := range entries {
		kinds[en.Kind]++
		if en.URL == "" || en.Label == "" {
			t.Errorf("empty entry %+v", en)
		}
	}
	if kinds["service"] == 0 || kinds["target"] == 0 || kinds["application"] == 0 || kinds["page"] < 10 {
		t.Errorf("kinds %v", kinds)
	}
	for _, en := range entries {
		if en.Label == "Workspace settings" {
			t.Error("a viewer is offered the admin page")
		}
	}
	if _, body = get(t, e.as(store.RoleAdmin), e.srv.URL+"/ui/palette.json", nil); !strings.Contains(body, "Workspace settings") {
		t.Error("an admin misses the admin page")
	}
	if !strings.Contains(body, `"url":"#shortcuts"`) || !strings.Contains(body, "tv=1") {
		t.Error("palette misses the shortcuts list or TV mode")
	}
}

// Updates, Delivery, Security and the tiles have a filter box; an application's cell is found by its services.
func TestPageFilters(t *testing.T) {
	e := newUIEnv(t)
	m := e.as(store.RoleMember)
	for _, path := range []string{"/updates", "/delivery", "/security", "/tiles"} {
		if _, page := get(t, m, e.srv.URL+path, nil); !strings.Contains(page, `class="search page-filter"`) || !strings.Contains(page, "data-filter-url") {
			t.Errorf("%s has no filter", path)
		}
	}
	if _, page := get(t, m, e.srv.URL+"/tiles?group=app", nil); !strings.Contains(page, `data-search="`) {
		t.Error("tiles cells carry nothing to filter on")
	}
}
