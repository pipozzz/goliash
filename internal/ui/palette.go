// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package ui

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"sort"
	"strings"

	"github.com/pipozzz/goliash/internal/auth"
	"github.com/pipozzz/goliash/internal/store"
	"github.com/pipozzz/goliash/internal/versions"
)

// The command palette (Ctrl+K or ⌘K, or the search button in the header) jumps to a
// service, an application, a target or a page. app.js loads its entries from here
// the first time it opens, and again after the workspace changed.

// PaletteEntry is one place the palette can go.
type PaletteEntry struct {
	Kind  string `json:"kind"`  // view, service, application, target, page
	Label string `json:"label"` // what is matched and shown
	Sub   string `json:"sub,omitempty"`
	URL   string `json:"url"`
	State string `json:"state,omitempty"` // a tile state, for a coloured mark
	// Keys are more words that find it without being shown: a service's images, running versions, workloads and
	// GitOps applications, so "nginx", "ghcr.io/acme", "1.27" or "payments-prod" find the service.
	Keys []string `json:"keys,omitempty"`
}

func (s *Server) palette(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	out, err := s.paletteEntries(r.Context(), p)
	if err != nil {
		return err
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "private, no-store")
	return json.NewEncoder(w).Encode(out)
}

// paletteEntries are the places the palette (and the search page) can go, for this person.
func (s *Server) paletteEntries(ctx context.Context, p auth.Principal) ([]PaletteEntry, error) {
	g, err := s.overview(ctx, p.Scope, "app")
	if err != nil {
		return nil, err
	}
	keys, err := s.paletteKeys(ctx, p.Scope, g)
	if err != nil {
		return nil, err
	}
	var out []PaletteEntry
	teams := map[string]int{}
	for _, row := range g.Rows {
		if row.Owner != "" {
			teams[row.Owner]++
		}
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
		out = append(out, PaletteEntry{Kind: "service", Label: row.Service, Sub: sub, URL: row.URL, State: c.State, Keys: keys[row.Service]})
	}
	// Services split by application appear once per application; keep one entry.
	out = dedupeEntries(out)
	v := TilesView{GroupBy: "app"}
	apps, _ := appItems(v, groupRows(g.Rows, "app"), g.Envs, "")
	for _, a := range apps {
		out = append(out, PaletteEntry{Kind: "application", Label: a.Name, Sub: itoa(a.OK) + " of " + itoa(a.Total) + " up to date", URL: a.Href, State: a.State})
	}
	for team, n := range teams {
		out = append(out, PaletteEntry{Kind: "team", Label: team, Sub: plural(n, "service", "services"), URL: "/tiles?group=team&app=" + url.QueryEscape(team)})
	}
	targets, err := s.store.ListTargets(ctx, p.Scope)
	if err != nil {
		return nil, err
	}
	for _, t := range targets {
		out = append(out, PaletteEntry{Kind: "target", Label: t.Name, Sub: t.Platform, URL: "/targets/" + t.ID})
	}
	for _, sv := range s.visibleViews(ctx, p) {
		sub := "your view"
		if sv.UserID == "" {
			sub = "for everyone"
		}
		out = append(out, PaletteEntry{Kind: "view", Label: sv.Name, Sub: sub, URL: sv.URL})
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
		{"Security", "exposure, end of life, blind spots", "/security"},
		{"Vulnerabilities", "known CVEs in running images", "/security/vulns"},
		{"Licenses", "packages in running images by license", "/security/licenses"},
		{"GitOps applications", "Argo CD and Flux, in sync or not", "/gitops"},
		{"Status", "is Goliash itself healthy", "/status"},
		{"Monthly report", "", "/report"},
		{"Vulnerability management report", "", "/security/report"},
		{"Agents and targets", "settings", "/agents"},
		{"Connect", "add a cluster or host", "/connect"},
		{"Notifications", "channels and rules", "/notifications"},
		{"Your account", "password, two-factor sign-in", "/account"},
		{"TV mode", "tiles for a wall display", "/tiles?group=app&tv=1"},
		{"Keyboard shortcuts", "press ? anywhere", "#shortcuts"},
	} {
		out = append(out, PaletteEntry{Kind: "page", Label: pg.label, Sub: pg.sub, URL: pg.url})
	}
	if p.Can(store.RoleAdmin) {
		for _, sec := range []struct{ label, sub, url string }{
			{"Workspace settings", "people, tokens, registries, audit log", "/settings"},
			{"People", "who can sign in, and their roles", "/settings#people"},
			{"API tokens", "for scripts, Prometheus, CI", "/settings#tokens"},
			{"Audit log", "changes to configuration, tokens and roles", "/settings#audit"},
			{"Apps with access", "AI assistants and CLIs signed in with OAuth", "/settings#apps"},
		} {
			out = append(out, PaletteEntry{Kind: "page", Label: sec.label, Sub: sec.sub, URL: sec.url})
		}
		out = append(out, PaletteEntry{Kind: "page", Label: "Private registries", Sub: "registry credentials for the server", URL: "/settings#registries"})
		out = append(out, PaletteEntry{Kind: "page", Label: "Change freezes", Sub: "hold automatic pull requests back", URL: "/settings#freezes"})
	}
	return out, nil
}

var kindOrder = map[string]int{"view": 0, "application": 1, "service": 2, "team": 3, "target": 4, "page": 5}

// paletteKeys are the words besides its name that find each service: the images and versions it runs, its
// workloads, and the GitOps applications that deploy it.
func (s *Server) paletteKeys(ctx context.Context, sc store.Scope, g MatrixGrid) (map[string][]string, error) {
	active, err := s.store.ListActiveInstances(ctx, sc)
	if err != nil {
		return nil, err
	}
	services, err := s.store.ListServices(ctx, sc)
	if err != nil {
		return nil, err
	}
	name := map[string]string{}
	for _, svc := range services {
		name[svc.ID] = svc.Name
	}
	seen := map[string]map[string]bool{}
	out := map[string][]string{}
	add := func(svc, k string) {
		k = strings.ToLower(strings.TrimSpace(k))
		if svc == "" || k == "" || k == strings.ToLower(svc) {
			return
		}
		if seen[svc] == nil {
			seen[svc] = map[string]bool{}
		}
		if !seen[svc][k] && len(out[svc]) < 24 {
			seen[svc][k] = true
			out[svc] = append(out[svc], k)
		}
	}
	for _, in := range active {
		svc := name[in.ServiceID]
		if !in.IsMain || svc == "" {
			continue
		}
		img := versions.ParseImage(in.Image)
		add(svc, img.Repo())
		add(svc, in.Tag)
		add(svc, in.WorkloadName)
	}
	for _, a := range g.gitops {
		add(a.Service, a.App)
	}
	return out, nil
}

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
