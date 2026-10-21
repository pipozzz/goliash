// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package ui

import (
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/pipozzz/goliash/internal/auth"
	"github.com/pipozzz/goliash/internal/versions"
)

// SearchView is one search across everything Goliash knows: places (services by what they run, applications,
// teams, targets, pages), vulnerabilities, packages, pull requests, history and notifications.
type SearchView struct {
	Base
	Query    string
	Sections []SearchSection
	Total    int
	Examples [][2]string // with nothing asked yet: what to try, from this workspace (kind, words)
}

// SearchSection is one kind of result: up to searchShown hits, how many there are, and where to see them all.
type SearchSection struct {
	Title     string
	Total     int
	Hits      []SearchHit
	More      string // the page listing all of them, filtered; "" when there is none
	MoreLabel string
	// Act is a page to act on them from: the upgrades of the services found.
	Act, ActLabel string
}

// SearchHit is one result.
type SearchHit struct {
	Label, Sub, URL string
	State           string // a tile state, for a coloured mark
	Badge           string // a short tag: critical, exploited, merged…
	External        bool   // the URL leaves Goliash (a pull request)
}

const searchShown = 8

func (sec *SearchSection) add(h SearchHit) {
	sec.Total++
	if len(sec.Hits) < searchShown {
		sec.Hits = append(sec.Hits, h)
	}
}

func (s *Server) search(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	ctx := r.Context()
	v := SearchView{Base: s.base(ctx, p, "", "Search"), Query: strings.TrimSpace(r.URL.Query().Get("q"))}
	if v.Query == "" {
		if entries, err := s.paletteEntries(ctx, p); err == nil {
			seen := map[string]bool{}
			for _, e := range entries {
				switch {
				case e.Kind == "service" && !seen["a service"]:
					seen["a service"] = true
					v.Examples = append(v.Examples, [2]string{"a service", e.Label})
					for _, k := range e.Keys {
						if strings.Contains(k, "/") && !seen["an image"] {
							seen["an image"] = true
							v.Examples = append(v.Examples, [2]string{"an image", k})
						}
					}
				case e.Kind == "team" && !seen["a team"]:
					seen["a team"] = true
					v.Examples = append(v.Examples, [2]string{"a team", e.Label})
				}
			}
		}
		return render(w, r, SearchPage(v))
	}
	match := versions.Matcher(v.Query)
	q := url.QueryEscape(v.Query)

	// Places, as the palette has them: a service is found by its images, versions, workloads and GitOps apps too.
	entries, err := s.paletteEntries(ctx, p)
	if err != nil {
		return err
	}
	places := map[string]*SearchSection{
		"service": {
			Title: "Services", More: "/?view=table&q=" + q, MoreLabel: "Filter the matrix",
			Act: "/updates?q=" + q, ActLabel: "Their upgrades",
		},
		"application": {Title: "Applications and teams", More: "/tiles?q=" + q, MoreLabel: "Filter the tiles"},
		"target":      {Title: "Targets"},
		"page":        {Title: "Pages and views"},
	}
	places["team"], places["view"] = places["application"], places["page"]
	for _, e := range entries {
		sec := places[e.Kind]
		if sec == nil || !match(append([]string{e.Label, e.Sub}, e.Keys...)...) {
			continue
		}
		h := SearchHit{Label: e.Label, Sub: e.Sub, URL: e.URL, State: e.State}
		if !match(e.Label, e.Sub) { // found by what it runs: say which
			for _, k := range e.Keys {
				if match(k) {
					h.Sub = "runs " + k
					break
				}
			}
		}
		sec.add(h)
	}
	for _, k := range []string{"service", "application", "target", "page"} {
		v.Sections = append(v.Sections, *places[k])
	}

	// The rest is what the API and assistants search too; its services are already here, with their states.
	found, err := versions.Search(ctx, s.store, p.Scope, v.Query, searchShown, time.Now())
	if err != nil {
		return err
	}
	for _, f := range found {
		if f.Kind == "services" {
			continue
		}
		sec := SearchSection{Title: f.Title, Total: f.Total, More: f.More, MoreLabel: f.MoreLabel}
		for _, h := range f.Hits {
			sec.Hits = append(sec.Hits, SearchHit{Label: h.Label, Sub: h.Sub, URL: h.URL, Badge: h.Badge, External: h.External})
		}
		v.Sections = append(v.Sections, sec)
	}
	for _, sec := range v.Sections {
		v.Total += sec.Total
	}
	return render(w, r, SearchPage(v))
}

// searchBadge colours a hit's tag.
func searchBadge(b string) string {
	switch b {
	case "exploited", "critical", "high", "failing":
		return "eol"
	case "merged", "passing":
		return "ok"
	case "open":
		return "upstream"
	}
	return "neutral"
}
