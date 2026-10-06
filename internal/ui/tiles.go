// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package ui

import (
	"net/http"
	"sort"
	"strings"

	"github.com/pipozzz/goliash/internal/auth"
)

// The tiles view draws the matrix the way the logo does: one rounded tile per
// application (or team, or status), and inside it one cell per service, coloured
// like the logo's cells: bright when current, dimmer when behind, faint when nothing
// is known, warm when it needs attention.

// TilesView is the tiles page.
type TilesView struct {
	Base
	GroupBy string
	Style   string // board (the logo, enlarged) or icons (an icon per application)
	Tiles   []Tile
	Counts  TileCounts
}

// TileCounts sum the services by state, for the legend.
type TileCounts struct{ Current, Behind, Attention, Unknown int }

// Tile is one application (team, status) with its services.
type Tile struct {
	ID, Name, Caption string
	State             string // current, behind, attention or unknown: the worst of its services
	Cells             []TileCell
	OK, Total         int
	Headline          string // its most urgent upgrade, "gitea 1.27.3 → 28.0.0"
}

// TileCell is one service in a tile.
type TileCell struct {
	Service, URL, App, Owner string
	State                    string
	Running                  string // in the highest environment it runs in
	Env                      string
	Latest                   string
	Badges                   []DriftBadge
}

// tileState turns a matrix row's health into the logo's colours: a row with
// nothing to compare with (no upstream known, no drift) is "unknown".
func tileState(r MatrixRow) string {
	switch r.Health {
	case "bad":
		return "attention"
	case "warn":
		return "behind"
	}
	if r.Latest == "" {
		return "unknown"
	}
	return "current"
}

var stateRank = map[string]int{"attention": 0, "behind": 1, "unknown": 2, "current": 3}

func (s *Server) tiles(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	g, err := s.overview(r.Context(), p.Scope, matrixGroupBy(r.URL.Query().Get("group")))
	if err != nil {
		return err
	}
	v := TilesView{Base: s.base(r.Context(), p, "matrix", "Tiles"), GroupBy: g.GroupBy, Style: "board"}
	if r.URL.Query().Get("style") == "icons" {
		v.Style = "icons"
	}
	if v.GroupBy == "none" {
		v.GroupBy = "app"
		g.Groups = groupRows(g.Rows, "app")
	}
	for i, grp := range g.Groups {
		t := Tile{ID: "tile-" + itoa(i), Name: grp.Name, Caption: grp.Caption, State: "current", Total: len(grp.Rows)}
		for _, row := range grp.Rows {
			c := TileCell{Service: row.Service, URL: row.URL, App: row.App, Owner: row.Owner, State: tileState(row), Latest: row.Latest}
			for ei := len(row.Cells) - 1; ei >= 0; ei-- {
				if len(row.Cells[ei].Versions) > 0 {
					c.Running, c.Env = row.Cells[ei].Versions[0].Tag, g.Envs[ei].Name
					if res := row.Cells[ei].Versions[0].Resolved; res != "" {
						c.Running += " = " + res
					}
					break
				}
			}
			seen := map[string]bool{}
			for _, cell := range row.Cells {
				for _, b := range cell.Drifts {
					if !seen[b.Label] {
						seen[b.Label] = true
						c.Badges = append(c.Badges, b)
					}
				}
			}
			if c.State == "current" {
				t.OK++
			}
			switch c.State {
			case "current":
				v.Counts.Current++
			case "behind":
				v.Counts.Behind++
			case "attention":
				v.Counts.Attention++
			default:
				v.Counts.Unknown++
			}
			if stateRank[c.State] < stateRank[t.State] {
				t.State = c.State
			}
			t.Cells = append(t.Cells, c)
		}
		sort.SliceStable(t.Cells, func(a, b int) bool { return stateRank[t.Cells[a].State] < stateRank[t.Cells[b].State] })
		if c := t.Cells[0]; c.State == "attention" || c.State == "behind" {
			t.Headline = c.Service + " " + c.Running
			if c.Latest != "" {
				t.Headline += " → " + c.Latest
			}
		}
		v.Tiles = append(v.Tiles, t)
	}
	return render(w, r, TilesPage(v))
}

// tileGridSize is the side of the square grid a tile's cells sit in: 2×2 for up to
// four services, 3×3 up to nine, and so on, so every tile looks like the logo.
func tileGridSize(n int) int {
	side := 1
	for side*side < n {
		side++
	}
	return max(side, 2)
}

func tileLabel(state string) string {
	return map[string]string{
		"current": "up to date", "behind": "behind", "attention": "needs attention", "unknown": "nothing to compare with",
	}[state]
}

func tilesURL(group string) string { return "/tiles?group=" + group }

func tilesStyleURL(v TilesView, style string) string { return tilesURL(v.GroupBy) + "&style=" + style }

func shortName(s string) string {
	if len(s) > 22 {
		return strings.TrimSpace(s[:21]) + "…"
	}
	return s
}

// boardColumns keeps the board close to a square, like the logo: 3 columns for up to
// nine applications, 4 up to sixteen, and so on, never more than 6.
func boardColumns(n int) int {
	return min(max(tileGridSize(n), 3), 6)
}
