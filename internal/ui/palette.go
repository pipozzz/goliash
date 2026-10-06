// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package ui

import (
	"encoding/json"
	"net/http"
	"sort"

	"github.com/pipozzz/goliash/internal/auth"
	"github.com/pipozzz/goliash/internal/store"
)

// The command palette (Ctrl+K or ⌘K, or the search button in the header) jumps to a
// service, an application, a target or a page. app.js loads its entries from here
// the first time it opens, and again after the workspace changed.

// PaletteEntry is one place the palette can go.
type PaletteEntry struct {
	Kind  string `json:"kind"`  // service, application, target, page
	Label string `json:"label"` // what is matched and shown
	Sub   string `json:"sub,omitempty"`
	URL   string `json:"url"`
	State string `json:"state,omitempty"` // a tile state, for a coloured mark
}

func (s *Server) palette(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	ctx := r.Context()
	g, err := s.overview(ctx, p.Scope, "app")
	if err != nil {
		return err
	}
	var out []PaletteEntry
	for _, row := range g.Rows {
		c, running, env := service(row, g.Envs, "")
		sub := row.Owner
		if running != "" {
			if sub != "" {
				sub += " · "
			}
			sub += env + " " + running
		}
		if row.Split && row.App != "" {
			sub = "in " + row.App + " · " + sub
		}
		out = append(out, PaletteEntry{Kind: "service", Label: row.Service, Sub: sub, URL: row.URL, State: c.State})
	}
	// Services split by application appear once per application; keep one entry.
	out = dedupeEntries(out)
	v := TilesView{GroupBy: "app"}
	apps, _ := appItems(v, groupRows(g.Rows, "app"), g.Envs, "")
	for _, a := range apps {
		out = append(out, PaletteEntry{Kind: "application", Label: a.Name, Sub: itoa(a.OK) + " of " + itoa(a.Total) + " up to date", URL: a.Href, State: a.State})
	}
	targets, err := s.store.ListTargets(ctx, p.Scope)
	if err != nil {
		return err
	}
	for _, t := range targets {
		out = append(out, PaletteEntry{Kind: "target", Label: t.Name, Sub: t.Platform, URL: "/targets/" + t.ID})
	}
	sort.SliceStable(out, func(i, j int) bool { return kindOrder[out[i].Kind] < kindOrder[out[j].Kind] })
	for _, pg := range []struct{ label, sub, url string }{
		{"Matrix", "services × environments", "/?view=table"},
		{"Tiles", "the matrix drawn like the logo", "/tiles"},
		{"Updates", "what to upgrade first", "/updates"},
		{"Inbox", "workloads to map to services", "/inbox"},
		{"Delivery", "promotions and deploys", "/delivery"},
		{"History", "every change", "/events"},
		{"Hygiene", "moving tags and untrusted registries", "/hygiene"},
		{"Monthly report", "", "/report"},
		{"Agents and targets", "settings", "/agents"},
		{"Connect", "add a cluster or host", "/connect"},
		{"Notifications", "channels and rules", "/notifications"},
		{"Your account", "password, two-factor sign-in", "/account"},
	} {
		out = append(out, PaletteEntry{Kind: "page", Label: pg.label, Sub: pg.sub, URL: pg.url})
	}
	if p.Can(store.RoleAdmin) {
		out = append(out, PaletteEntry{Kind: "page", Label: "Users and API tokens", Sub: "settings", URL: "/settings"})
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "private, no-store")
	return json.NewEncoder(w).Encode(out)
}

var kindOrder = map[string]int{"application": 0, "service": 1, "target": 2, "page": 3}

func dedupeEntries(in []PaletteEntry) []PaletteEntry {
	seen := map[string]bool{}
	out := in[:0]
	for _, e := range in {
		if seen[e.Kind+"|"+e.URL] {
			continue
		}
		seen[e.Kind+"|"+e.URL] = true
		out = append(out, e)
	}
	return out
}
