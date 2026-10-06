// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package ui

import (
	"crypto/sha256"
	"encoding/hex"
	"math"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/pipozzz/goliash/internal/auth"
)

// The tiles view draws the matrix the way the logo does: a navy board of rounded
// cells, one per application (or team, or status), coloured like the logo's cells:
// bright when current, dimmer when behind, faint when nothing is known, warm when it
// needs attention. A cell opens the same board for that application, one cell per
// service; a service opens its details. Every cell carries a strip with its state
// in each environment, and the board can be drawn per environment, side by side.

// Cell states, worst first.
const (
	stAttention = "attention"
	stBehind    = "behind"
	stUnknown   = "unknown"
	stCurrent   = "current"
	stNone      = "none" // does not run there
)

var stateRank = map[string]int{stAttention: 0, stBehind: 1, stUnknown: 2, stCurrent: 3, stNone: 4}

func worse(a, b string) string {
	if stateRank[b] < stateRank[a] {
		return b
	}
	return a
}

// TilesView is the tiles page.
type TilesView struct {
	Base
	GroupBy    string
	Env        string // "" for every environment, an environment's name, or "side" for one board per environment
	Envs       []string
	App        string // the application (team, status) drilled into; empty on the global board
	AppNote    string
	Items      []BoardItem
	Parent     []BoardItem // drilled in: the global board, drawn behind as the way back and across
	AppItem    BoardItem   // drilled in: the application's own cell
	ParentCols int
	Boards     []EnvBoard // Env == "side": one board per environment, same cells in the same places
	Panels     []TileCell // the services' details (drilled in)
	Counts     TileCounts
	Cols       int
	Rows       int // how many rows of cells the board has, for TV mode to fit the screen
	TV         bool
	Updated    string
}

// EnvBoard is one environment's board when they are drawn side by side.
type EnvBoard struct {
	Env    string
	Items  []BoardItem
	Counts TileCounts
}

// TileCounts sum the services by state, for the legend.
type TileCounts struct{ Current, Behind, Attention, Unknown int }

func (c *TileCounts) add(state string) {
	switch state {
	case stCurrent:
		c.Current++
	case stBehind:
		c.Behind++
	case stAttention:
		c.Attention++
	case stUnknown:
		c.Unknown++
	}
}

// BoardItem is one cell of a board: an application on the global board, a service
// when drilled in.
type BoardItem struct {
	Key, Name, Caption, Href string
	State                    string
	Envs                     []TileEnv // its state in each environment
	Dots                     []string  // its services' states (applications only)
	Headline                 string    // its most urgent upgrade
	OK, Total                int
	Span                     int    // 2 for a big application: a 2×2 cell
	Current                  bool   // the application drilled into, on the board behind
	VT                       string // its view-transition name, so the cell grows into its card
	VTFront                  string
}

// TileEnv is a cell's state in one environment.
type TileEnv struct{ Env, State, Title string }

// TileCell is one service, for the icons and the details panel.
type TileCell struct {
	Key, Service, URL, Owner, App string
	State                         string
	Envs                          []TileEnv
	Latest, LatestURL             string
	Badges                        []DriftBadge
}

// envState is a matrix cell's state: warm for end of life or targets disagreeing,
// behind for any other drift, unknown without an upstream to compare with.
func envState(c MatrixCell, latest string) string {
	if len(c.Versions) == 0 {
		return stNone
	}
	st := stCurrent
	if latest == "" {
		st = stUnknown
	}
	for _, d := range c.Drifts {
		if d.Kind == "eol" || d.Kind == "inconsistent" {
			return stAttention
		}
		st = stBehind
	}
	return st
}

func versionOf(c MatrixCell) string {
	if len(c.Versions) == 0 {
		return ""
	}
	v := c.Versions[0].Tag
	if c.Versions[0].Resolved != "" {
		v += " = " + c.Versions[0].Resolved
	}
	return v
}

// service turns a matrix row into a tile cell, with its state in env (all when empty).
func service(r MatrixRow, envs []EnvHeader, env string) (TileCell, string, string) {
	c := TileCell{
		Key: slug("svc-" + r.Service + "-" + r.App), Service: r.Service, URL: r.URL, Owner: r.Owner, App: r.App,
		Latest: r.Latest, LatestURL: r.LatestURL, State: stNone,
	}
	running, runEnv := "", ""
	seen := map[string]bool{}
	for i, cell := range r.Cells {
		st := envState(cell, r.Latest)
		title := envs[i].Name + ": "
		if st == stNone {
			title += "not running"
		} else {
			title += versionOf(cell) + " · " + tileLabel(st)
		}
		c.Envs = append(c.Envs, TileEnv{Env: envs[i].Name, State: st, Title: title})
		if env == "" || env == envs[i].Name {
			c.State = worse(c.State, st)
			if st != stNone {
				running, runEnv = versionOf(cell), envs[i].Name
			}
		}
		for _, b := range cell.Drifts {
			if !seen[b.Label] {
				seen[b.Label] = true
				c.Badges = append(c.Badges, b)
			}
		}
	}
	return c, running, runEnv
}

func (s *Server) tiles(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	q := r.URL.Query()
	g, err := s.overview(r.Context(), p.Scope, matrixGroupBy(q.Get("group")))
	if err != nil {
		return err
	}
	v := TilesView{
		Base: s.base(r.Context(), p, "matrix", "Tiles"), GroupBy: g.GroupBy, Env: q.Get("env"),
		App: q.Get("app"), TV: q.Get("tv") == "1", Updated: time.Now().UTC().Format("15:04 UTC"),
	}
	v.Kiosk = v.TV
	if v.GroupBy == "none" {
		v.GroupBy = "app"
	}
	for _, e := range g.Envs {
		v.Envs = append(v.Envs, e.Name)
	}
	if v.Env != "side" && !slices.Contains(v.Envs, v.Env) {
		v.Env = ""
	}
	// Remember the tiles as this browser's home view.
	http.SetCookie(w, &http.Cookie{ //nolint:gosec // Secure follows the public URL scheme
		Name: "goliash_view", Value: "tiles", Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode,
		Secure: strings.HasPrefix(s.publicURL, "https://"), MaxAge: 365 * 24 * 3600,
	})

	env := v.Env
	if env == "side" {
		env = ""
	}
	groups := groupRows(g.Rows, v.GroupBy)
	if v.App != "" {
		var rows []MatrixRow
		for _, grp := range groups {
			if grp.Name == v.App {
				rows, v.AppNote = grp.Rows, grp.Caption
			}
		}
		for _, row := range rows {
			c, running, runEnv := service(row, g.Envs, env)
			if c.State == stNone {
				continue
			}
			it := BoardItem{Key: c.Key, Name: c.Service, Caption: c.Owner, Href: "#" + c.Key, State: c.State, Envs: c.Envs, Total: 1, Span: 1}
			if c.State == stCurrent {
				it.OK = 1
			}
			if running != "" {
				it.Headline = running
				if c.State != stCurrent && c.Latest != "" {
					it.Headline += " → " + c.Latest
				}
				if v.Env == "" && len(g.Envs) > 1 {
					it.Headline = runEnv + " " + it.Headline
				}
			}
			v.Counts.add(c.State)
			v.Items = append(v.Items, it)
			v.Panels = append(v.Panels, c)
		}
	}
	apps, appCounts := appItems(v, groups, g.Envs, env)
	if v.App == "" {
		v.Items, v.Counts = apps, appCounts
	} else {
		for i := range apps {
			apps[i].Span = 1
			if apps[i].Current {
				v.AppItem = apps[i]
			}
		}
		v.Parent, v.ParentCols = apps, boardColumns(len(apps))
	}
	if len(v.Items) < 6 { // a few applications: big cells would leave the board half empty
		for i := range v.Items {
			v.Items[i].Span = 1
		}
	}
	units := 0
	for _, it := range v.Items {
		units += it.Span * it.Span
	}
	v.Cols = boardColumns(units)
	if v.TV { // a wall screen is wide: more columns, fewer rows, bigger cells
		v.Cols = min(max(int(math.Ceil(math.Sqrt(float64(units)*1.8))), 3), 8)
	}
	v.Rows = max((units+v.Cols-1)/v.Cols, 1)
	for _, it := range v.Items {
		if it.Span == 2 { // big cells leave gaps the dense flow cannot always fill
			v.Rows++
			break
		}
	}
	if v.Env == "side" {
		for i, e := range v.Envs {
			b := EnvBoard{Env: e}
			for _, it := range v.Items {
				cp := it
				cp.State = it.Envs[i].State
				cp.Span = 1
				b.Counts.add(cp.State)
				b.Items = append(b.Items, cp)
			}
			v.Boards = append(v.Boards, b)
		}
	}
	return render(w, r, TilesPage(v))
}

// tilesHref links to the tiles with the page's choices, drilled into app (or not).
func tilesHref(v TilesView, app string) string {
	q := url.Values{"group": {v.GroupBy}}
	if v.Env != "" {
		q.Set("env", v.Env)
	}
	if app != "" {
		q.Set("app", app)
	}
	if v.TV {
		q.Set("tv", "1")
	}
	return "/tiles?" + q.Encode()
}

// tilesWith is tilesHref with one choice changed.
func tilesWith(v TilesView, key, value string) string {
	switch key {
	case "group":
		v.GroupBy, v.App = value, ""
	case "env":
		v.Env = value
	case "tv":
		v.TV = value == "1"
	}
	return tilesHref(v, v.App)
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

// boardColumns keeps the board close to a square, like the logo: 3 columns for up to
// nine units of area, 4 up to sixteen, and so on, never more than 6.
func boardColumns(units int) int {
	return min(max(tileGridSize(units), 3), 6)
}

func tileLabel(state string) string {
	return map[string]string{
		stCurrent: "up to date", stBehind: "behind", stAttention: "needs attention", stUnknown: "nothing to compare with",
		stNone: "not running",
	}[state]
}

func slug(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	return b.String()
}

func shortName(s string) string {
	if len([]rune(s)) > 22 {
		return strings.TrimSpace(string([]rune(s)[:21])) + "…"
	}
	return s
}

func groupWord(by string) string {
	return map[string]string{"app": "applications", "team": "teams", "status": "states"}[by]
}

// favicon is the logo coloured by the workspace's state: of its nine cells, as many
// are bright, dim, faint and warm as the services are current, behind, unknown and in
// need of attention (at least one warm cell when any service needs attention), the
// warm ones where the logo has its warm cell.
//
// Every page asks for it, so it is kept per workspace until the workspace changes
// (or for faviconTTL, for changes another server's agents brought without a relay),
// and the browser revalidates it with an ETag.
func (s *Server) favicon(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	ws := p.Scope.WorkspaceID
	ver := s.hub.Version(ws)
	s.favMu.Lock()
	c, ok := s.favicons[ws]
	s.favMu.Unlock()
	if !ok || c.version != ver || time.Since(c.at) > faviconTTL {
		g, err := s.overview(r.Context(), p.Scope, "none")
		if err != nil {
			return err
		}
		var counts TileCounts
		for _, row := range g.Rows {
			cell, _, _ := service(row, g.Envs, "")
			counts.add(cell.State)
		}
		svg := faviconSVG(counts)
		sum := sha256.Sum256([]byte(svg))
		c = cachedFavicon{svg: svg, etag: `"` + hex.EncodeToString(sum[:8]) + `"`, version: ver, at: time.Now()}
		s.favMu.Lock()
		if s.favicons == nil {
			s.favicons = map[string]cachedFavicon{}
		}
		s.favicons[ws] = c
		s.favMu.Unlock()
	}
	w.Header().Set("Content-Type", "image/svg+xml")
	w.Header().Set("Cache-Control", "private, no-cache")
	w.Header().Set("ETag", c.etag)
	if r.Header.Get("If-None-Match") == c.etag {
		w.WriteHeader(http.StatusNotModified)
		return nil
	}
	_, err := w.Write([]byte(c.svg))
	return err
}

// faviconTTL bounds how long a workspace's favicon is reused without a change heard of.
const faviconTTL = 2 * time.Minute

type cachedFavicon struct {
	svg, etag string
	version   uint64
	at        time.Time
}

func faviconSVG(c TileCounts) string {
	total := c.Current + c.Behind + c.Attention + c.Unknown
	cells := make([]string, 0, 9)
	share := func(n int) int {
		if total == 0 || n == 0 {
			return 0
		}
		return max(1, (n*9+total/2)/total)
	}
	for _, part := range []struct {
		n     int
		state string
	}{{c.Attention, stAttention}, {c.Behind, stBehind}, {c.Unknown, stUnknown}} {
		for range share(part.n) {
			if len(cells) < 8 || part.state == stAttention {
				cells = append(cells, part.state)
			}
		}
	}
	for len(cells) < 9 {
		cells = append(cells, stCurrent)
	}
	cells = cells[:9]
	// The logo's warm cell is the middle right; fill the others from there.
	order := []int{5, 1, 7, 3, 2, 6, 0, 8, 4}
	grid := make([]string, 9)
	for i, idx := range order {
		grid[idx] = cells[i]
	}
	var b strings.Builder
	b.WriteString(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 96 96"><rect width="96" height="96" rx="22" fill="#1b2a6b"/>`)
	for i, st := range grid {
		x, y := 17+22*(i%3), 17+22*(i/3)
		fill, op := "#8aa4ff", "1"
		switch st {
		case stAttention:
			fill = "#ffb347"
		case stBehind:
			op = ".55"
		case stUnknown:
			op = ".3"
		}
		b.WriteString(`<rect x="` + itoa(x) + `" y="` + itoa(y) + `" width="18" height="18" rx="4.5" fill="` + fill + `" opacity="` + op + `"/>`)
	}
	b.WriteString(`</svg>`)
	return b.String()
}

// cellLabel is what a screen reader says for a board cell.
func cellLabel(it BoardItem) string {
	s := it.Name + ", " + tileLabel(it.State)
	if it.Total > 1 {
		s += ", " + itoa(it.OK) + " of " + itoa(it.Total) + " services up to date"
	}
	if it.Headline != "" {
		s += ", " + it.Headline
	}
	return s
}

// tileGroup is the tiles' grouping for a matrix grouping: tiles always group.
func tileGroup(by string) string {
	if by == "none" {
		return "app"
	}
	return by
}

// appItems are the global board's cells: one per application (team, status) with
// its services' states in env (every environment when empty).
func appItems(v TilesView, groups []MatrixGroup, envs []EnvHeader, env string) ([]BoardItem, TileCounts) {
	var items []BoardItem
	var counts TileCounts
	for _, grp := range groups {
		it := BoardItem{Key: slug("app-" + grp.Name), Name: grp.Name, Caption: grp.Caption, State: stNone, Span: 1}
		it.Href = tilesHref(v, grp.Name)
		it.Current = grp.Name == v.App
		if v.App == "" {
			it.VT = "tile-" + it.Key // on the global board: grows into the card it opens
		}
		it.VTFront = "tile-" + it.Key
		envStates := make([]string, len(envs))
		for i := range envStates {
			envStates[i] = stNone
		}
		var worst TileCell
		worstRunning := ""
		worst.State = stNone
		for _, row := range grp.Rows {
			c, running, _ := service(row, envs, env)
			for i, e := range c.Envs {
				envStates[i] = worse(envStates[i], e.State)
			}
			if c.State == stNone {
				continue
			}
			it.Total++
			if c.State == stCurrent {
				it.OK++
			}
			it.Dots = append(it.Dots, c.State)
			it.State = worse(it.State, c.State)
			if stateRank[c.State] < stateRank[worst.State] {
				worst, worstRunning = c, running
			}
			counts.add(c.State)
		}
		if it.Total == 0 {
			continue
		}
		sort.Slice(it.Dots, func(a, b int) bool { return stateRank[it.Dots[a]] < stateRank[it.Dots[b]] })
		for i, st := range envStates {
			it.Envs = append(it.Envs, TileEnv{Env: envs[i].Name, State: st, Title: envs[i].Name + ": " + tileLabel(st)})
		}
		if worst.State == stAttention || worst.State == stBehind {
			it.Headline = worst.Service + " " + worstRunning
			if worst.Latest != "" {
				it.Headline += " → " + worst.Latest
			}
		}
		if it.Total >= 4 && !v.TV {
			it.Span = 2
		}
		items = append(items, it)
	}
	return items, counts
}
