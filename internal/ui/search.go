// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package ui

import (
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/pipozzz/goliash/internal/auth"
	"github.com/pipozzz/goliash/internal/store"
	"github.com/pipozzz/goliash/internal/versions"
)

// SearchView is one search across everything Goliash knows: places (services by what they run, applications,
// teams, targets, pages), vulnerabilities, packages, pull requests, history and notifications.
type SearchView struct {
	Base
	Query    string
	Sections []SearchSection
	Total    int
}

// SearchSection is one kind of result: up to searchShown hits, how many there are, and where to see them all.
type SearchSection struct {
	Title     string
	Total     int
	Hits      []SearchHit
	More      string // the page listing all of them, filtered; "" when there is none
	MoreLabel string
}

// SearchHit is one result.
type SearchHit struct {
	Label, Sub, URL string
	State           string // a tile state, for a coloured mark
	Badge           string // a short tag: critical, exploited, merged…
	External        bool   // the URL leaves Goliash (a pull request)
}

const searchShown = 8

// matcher reports whether every word of q is in one of the texts.
func matcher(q string) func(texts ...string) bool {
	words := strings.Fields(strings.ToLower(q))
	return func(texts ...string) bool {
		hay := strings.ToLower(strings.Join(texts, " "))
		for _, w := range words {
			if !strings.Contains(hay, w) {
				return false
			}
		}
		return len(words) > 0
	}
}

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
		return render(w, r, SearchPage(v))
	}
	match := matcher(v.Query)
	q := url.QueryEscape(v.Query)

	// Places, as the palette has them: a service is found by its images, versions, workloads and GitOps apps too.
	entries, err := s.paletteEntries(ctx, p)
	if err != nil {
		return err
	}
	places := map[string]*SearchSection{
		"service":     {Title: "Services", More: "/?view=table&q=" + q, MoreLabel: "Filter the matrix"},
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

	o, err := versions.LoadOverview(ctx, s.store, p.Scope)
	if err != nil {
		return err
	}
	vulns := SearchSection{Title: "Vulnerabilities", More: "/security/vulns?q=" + q, MoreLabel: "All in Vulnerabilities"}
	if rep, err := versions.LoadVulnReport(ctx, s.store, p.Scope, ""); err == nil {
		for _, f := range rep.Findings {
			var pkgs, svcs []string
			for _, a := range f.Affected {
				pkgs, svcs = append(pkgs, a.Package), append(svcs, a.Service)
			}
			texts := append(append(append([]string{f.Key, f.Summary}, f.IDs...), f.Aliases...), append(pkgs, svcs...)...)
			if !match(texts...) {
				continue
			}
			h := SearchHit{Label: f.Key, Sub: strings.Join(dedupe(svcs), ", ") + " · " + strings.Join(dedupe(pkgs), ", "), URL: "/security/vulns?q=" + url.QueryEscape(f.Key), Badge: strings.ToLower(f.Severity)}
			if f.Exploited != nil {
				h.Badge = "exploited"
			}
			vulns.add(h)
		}
	}
	pkgs := SearchSection{Title: "Packages", More: "/security/licenses?q=" + q, MoreLabel: "All in Licenses"}
	if rep, err := versions.LoadLicenseReport(ctx, s.store, p.Scope, ""); err == nil {
		for _, pk := range rep.Packages {
			if !match(pk.Name, pk.Purl, pk.License) {
				continue
			}
			var where []string
			for _, u := range pk.Where {
				where = append(where, u.Service)
			}
			lic := pk.License
			if lic == "" {
				lic = "no license named"
			}
			pkgs.add(SearchHit{Label: pk.Name + " " + pk.Version, Sub: lic + " · " + strings.Join(dedupe(where), ", "), URL: "/security/licenses?q=" + url.QueryEscape(pk.Name)})
		}
	}
	v.Sections = append(v.Sections, vulns, pkgs)

	prs := SearchSection{Title: "Pull requests", More: "/delivery?q=" + q, MoreLabel: "On the Delivery page"}
	if pulls, err := s.store.ListGitOpsPulls(ctx, p.Scope); err == nil {
		for _, pr := range pulls {
			svc := o.Services[pr.ServiceID].Name
			if !match(svc, pr.Environment, pr.From, pr.To, pr.URL, pr.OpenedBy, pr.State) {
				continue
			}
			prs.add(SearchHit{Label: svc + " " + pr.From + " → " + pr.To, Sub: pr.Environment + " · opened by " + pr.OpenedBy, URL: pr.URL, Badge: pr.State, External: true})
		}
	}
	events := SearchSection{Title: "History (90 days)"}
	if evs, err := s.store.ListEvents(ctx, p.Scope, store.EventFilter{Since: time.Now().AddDate(0, 0, -90), Limit: 3000}); err == nil {
		for _, e := range evs {
			svc, env := o.Services[e.ServiceID].Name, o.Envs[e.EnvironmentID].Name
			note := versions.EventNote(e)
			if !match(svc, env, e.FromVersion, e.ToVersion, e.Type, note, e.Actor) {
				continue
			}
			label := svc + " " + strings.TrimSpace(e.FromVersion+" → "+e.ToVersion)
			if e.FromVersion == "" {
				label = svc + " " + e.ToVersion
			}
			events.add(SearchHit{Label: strings.TrimSpace(label), Sub: strings.ReplaceAll(e.Type, "_", " ") + " · " + env + " · " + e.At.UTC().Format("2 Jan 15:04") + " " + note, URL: "/services/" + url.PathEscape(svc)})
		}
	}
	sent := SearchSection{Title: "Notifications (30 days)", More: "/notifications/deliveries?q=" + q, MoreLabel: "In the delivery log"}
	if ds, err := s.store.RecentDeliveries(ctx, p.Scope, 2000); err == nil {
		now := time.Now().UTC()
		for _, d := range ds {
			dv := deliveryView(d, now)
			if !match(dv.Text, dv.Channel) {
				continue
			}
			sent.add(SearchHit{Label: dv.Text, Sub: dv.Channel + " · " + d.CreatedAt.UTC().Format("2 Jan 15:04"), URL: "/notifications/deliveries?q=" + q})
		}
	}
	v.Sections = append(v.Sections, prs, events, sent)
	for _, sec := range v.Sections {
		v.Total += sec.Total
	}
	return render(w, r, SearchPage(v))
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
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
