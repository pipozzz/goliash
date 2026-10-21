// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package versions

import (
	"context"
	"encoding/json"
	"net/url"
	"strings"
	"time"

	"github.com/pipozzz/goliash/internal/store"
)

// SearchHit is one thing a search found: what to call it, a line about it, and where it is.
type SearchHit struct {
	Label    string `json:"label"`
	Sub      string `json:"detail,omitempty"`
	URL      string `json:"url"`                // a path in Goliash, or a pull request's address (External)
	Badge    string `json:"badge,omitempty"`    // critical, exploited, open, merged…
	External bool   `json:"external,omitempty"` // URL leaves Goliash
}

// SearchSection is one kind of result: how many, the first of them, and the page listing all, filtered.
type SearchSection struct {
	Kind  string      `json:"kind"` // services, vulnerabilities, packages, pull_requests, history, notifications
	Title string      `json:"title"`
	Total int         `json:"total"`
	Hits  []SearchHit `json:"hits"`
	More  string      `json:"more,omitempty"`
	// MoreLabel says what More shows.
	MoreLabel string `json:"-"`
	limit     int
}

func (sec *SearchSection) add(h SearchHit) {
	sec.Total++
	if len(sec.Hits) < sec.limit {
		sec.Hits = append(sec.Hits, h)
	}
}

// Matcher reports whether every word of q appears in the texts (case aside).
func Matcher(q string) func(texts ...string) bool {
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

// Search looks for q, every word of it, through what Goliash knows of the workspace: services by name, owner,
// image, version, workload and GitOps application; vulnerabilities of running images; their packages; the pull
// requests Goliash opened; the last 90 days of history and the last 30 of notifications. Each section keeps its
// first limit hits and counts them all.
func Search(ctx context.Context, st *store.Store, sc store.Scope, q string, limit int, now time.Time) ([]SearchSection, error) {
	match := Matcher(q)
	qq := url.QueryEscape(strings.TrimSpace(q))
	o, err := LoadOverview(ctx, st, sc)
	if err != nil {
		return nil, err
	}
	apps, _ := GitOpsApps(ctx, st, sc, o)

	// Services, by what they run too.
	keys := map[string][]string{}
	if active, err := st.ListActiveInstances(ctx, sc); err == nil {
		for _, in := range active {
			if in.IsMain && in.ServiceID != "" {
				keys[in.ServiceID] = append(keys[in.ServiceID], ParseImage(in.Image).Repo(), in.Tag, in.WorkloadName)
			}
		}
	}
	for _, a := range apps {
		keys[a.ServiceID] = append(keys[a.ServiceID], a.App)
	}
	services := SearchSection{Kind: "services", Title: "Services", More: "/?view=table&q=" + qq, MoreLabel: "Filter the matrix", limit: limit}
	for _, row := range o.Matrix.Rows {
		svc := row.Service
		if !match(append([]string{svc.Name, svc.Owner}, keys[svc.ID]...)...) {
			continue
		}
		h := SearchHit{Label: svc.Name, Sub: svc.Owner, URL: "/services/" + url.PathEscape(svc.Name)}
		if !match(svc.Name, svc.Owner) {
			for _, k := range keys[svc.ID] {
				if match(k) {
					h.Sub = "runs " + k
					break
				}
			}
		}
		if up, ok := o.Upstreams[svc.ID]; ok && up.HasLatest {
			h.Sub = strings.TrimPrefix(h.Sub+" · newest "+up.Latest.Raw, " · ")
		}
		services.add(h)
	}

	vulns := SearchSection{Kind: "vulnerabilities", Title: "Vulnerabilities", More: "/security/vulns?q=" + qq, MoreLabel: "All in Vulnerabilities", limit: limit}
	if rep, err := LoadVulnReport(ctx, st, sc, ""); err == nil {
		for _, f := range rep.Findings {
			var pkgs, svcs []string
			for _, a := range f.Affected {
				pkgs, svcs = append(pkgs, a.Package), append(svcs, a.Service)
			}
			if !match(append(append(append([]string{f.Key, f.Summary}, f.IDs...), f.Aliases...), append(pkgs, svcs...)...)...) {
				continue
			}
			h := SearchHit{
				Label: f.Key, Sub: strings.Join(unique(svcs), ", ") + " · " + strings.Join(unique(pkgs), ", "),
				URL: "/security/vulns?q=" + url.QueryEscape(f.Key), Badge: strings.ToLower(f.Severity),
			}
			if f.Exploited != nil {
				h.Badge = "exploited"
			}
			vulns.add(h)
		}
	}
	pkgs := SearchSection{Kind: "packages", Title: "Packages", More: "/security/licenses?q=" + qq, MoreLabel: "All in Licenses", limit: limit}
	if rep, err := LoadLicenseReport(ctx, st, sc, ""); err == nil {
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
			pkgs.add(SearchHit{Label: pk.Name + " " + pk.Version, Sub: lic + " · " + strings.Join(unique(where), ", "), URL: "/security/licenses?q=" + url.QueryEscape(pk.Name)})
		}
	}
	prs := SearchSection{Kind: "pull_requests", Title: "Pull requests", More: "/delivery?q=" + qq, MoreLabel: "On the Delivery page", limit: limit}
	if pulls, err := st.ListGitOpsPulls(ctx, sc); err == nil {
		for _, pr := range pulls {
			svc := o.Services[pr.ServiceID].Name
			if !match(svc, pr.Environment, pr.From, pr.To, pr.URL, pr.OpenedBy, pr.State) {
				continue
			}
			prs.add(SearchHit{Label: svc + " " + pr.From + " → " + pr.To, Sub: pr.Environment + " · opened by " + pr.OpenedBy, URL: pr.URL, Badge: pr.State, External: true})
		}
	}
	history := SearchSection{Kind: "history", Title: "History (90 days)", limit: limit}
	if evs, err := st.ListEvents(ctx, sc, store.EventFilter{Since: now.AddDate(0, 0, -90), Limit: 3000}); err == nil {
		for _, e := range evs {
			svc, env := o.Services[e.ServiceID].Name, o.Envs[e.EnvironmentID].Name
			note := EventNote(e)
			if !match(svc, env, e.FromVersion, e.ToVersion, e.Type, note, e.Actor) {
				continue
			}
			label := svc + " " + e.ToVersion
			if e.FromVersion != "" {
				label = svc + " " + e.FromVersion + " → " + e.ToVersion
			}
			sub := strings.ReplaceAll(e.Type, "_", " ")
			if env != "" {
				sub += " · " + env
			}
			history.add(SearchHit{Label: strings.TrimSpace(label), Sub: strings.TrimSpace(sub + " · " + e.At.UTC().Format("2 Jan 15:04") + " " + note), URL: "/services/" + url.PathEscape(svc)})
		}
	}
	sent := SearchSection{Kind: "notifications", Title: "Notifications (30 days)", More: "/notifications/deliveries?q=" + qq, MoreLabel: "In the delivery log", limit: limit}
	if ds, err := st.RecentDeliveries(ctx, sc, 2000); err == nil {
		for _, d := range ds {
			var it struct {
				Text string `json:"text"`
			}
			if json.Unmarshal(d.Payload, &it) != nil || !match(it.Text, d.ChannelName) {
				continue
			}
			sent.add(SearchHit{Label: it.Text, Sub: d.ChannelName + " · " + d.CreatedAt.UTC().Format("2 Jan 15:04"), URL: "/notifications/deliveries?q=" + qq})
		}
	}
	return []SearchSection{services, vulns, pkgs, prs, history, sent}, nil
}

func unique(in []string) []string {
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
