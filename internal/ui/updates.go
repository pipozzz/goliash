// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package ui

import (
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/pipozzz/goliash/internal/auth"
	"github.com/pipozzz/goliash/internal/versions"
)

// UpdatesView is the to-do list of upgrades.
type UpdatesView struct {
	Base
	Items, Acked []UpdateItem
	Levels       []UpdateLevel
	Level        int // the urgency shown; -1 for all
	Teams, Envs  []string
	Team, Env    string
	Markdown     string // the list as a markdown checklist, to copy into a ticket
	Total        int    // before filters
}

// UpdateLevel is one urgency with its count, for the tiles.
type UpdateLevel struct {
	Level        int
	Label, Class string
	Count        int
}

// UpdateItem is one upgrade.
type UpdateItem struct {
	Service, ServiceURL, App, Owner, Env string
	Running, Target, TargetURL           string
	Jump, Behind, EOL                    string
	EOLPassed                            bool
	On                                   string // the targets running Running, when others run newer versions
	Since                                time.Time
	Level                                int
	LevelClass                           string
	LevelLabel                           string
	Skip                                 string // the version after Target: "skip this version" holds until a newer one
}

var levelClasses = []string{"eol", "eol", "inconsistent", "upstream", "env", "neutral"}

func (s *Server) updates(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	ctx := r.Context()
	o, err := versions.LoadOverview(ctx, s.store, p.Scope)
	if err != nil {
		return err
	}
	acks, err := s.store.ListAcks(ctx, p.Scope)
	if err != nil {
		return err
	}
	q := r.URL.Query()
	v := UpdatesView{Base: withFlash(s.base(ctx, p, "updates", "Updates"), r), Team: q.Get("team"), Env: q.Get("env"), Level: -1}
	if l := q.Get("level"); l != "" {
		for i := range versions.UrgencyLabels {
			if itoa(i) == l {
				v.Level = i
			}
		}
	}
	teams, envs := map[string]bool{}, map[string]bool{}
	for i, label := range versions.UrgencyLabels {
		v.Levels = append(v.Levels, UpdateLevel{Level: i, Label: label, Class: levelClasses[i]})
	}
	var md strings.Builder
	for _, u := range versions.Updates(o, acks, time.Now()) {
		if u.Service.Owner != "" {
			teams[u.Service.Owner] = true
		}
		envs[u.Environment.Name] = true
		if (v.Team != "" && u.Service.Owner != v.Team) || (v.Env != "" && u.Environment.Name != v.Env) {
			continue
		}
		it := UpdateItem{
			Service: u.Service.Name, ServiceURL: serviceURL(u.Service.Name), App: u.App, Owner: u.Service.Owner,
			Env: u.Environment.Name, Running: u.Running, Target: u.Target, TargetURL: u.TargetURL, Jump: string(u.Jump),
			Behind: u.Behind, EOL: u.EOL, EOLPassed: u.EOLPassed, Since: u.Since, Level: u.Urgency, On: strings.Join(u.On, ", "),
			LevelLabel: versions.UrgencyLabels[u.Urgency], LevelClass: levelClasses[u.Urgency], Skip: nextVersion(u.Target),
		}
		if u.Acked {
			v.Acked = append(v.Acked, it)
			continue
		}
		v.Total++
		v.Levels[u.Urgency].Count++
		if v.Level >= 0 && u.Urgency != v.Level {
			continue
		}
		v.Items = append(v.Items, it)
		md.WriteString(updateMarkdown(it))
	}
	v.Teams, v.Envs = sortedSet(teams), sortedSet(envs)
	v.Markdown = md.String()
	return render(w, r, UpdatesPage(v))
}

// updateMarkdown is one checklist line: "- [ ] postgres (auth) @ prod: 15.6 → 15.8 (end of life 2026-09-01)".
func updateMarkdown(it UpdateItem) string {
	var b strings.Builder
	b.WriteString("- [ ] " + it.Service)
	if it.App != "" {
		b.WriteString(" (" + it.App + ")")
	}
	b.WriteString(" @ " + it.Env + ": " + orDash(it.Running))
	if it.On != "" {
		b.WriteString(" on " + it.On)
	}
	if it.Target != "" {
		b.WriteString(" → " + it.Target)
	}
	var why []string
	if it.EOL != "" {
		if it.EOLPassed {
			why = append(why, "end of life since "+it.EOL)
		} else {
			why = append(why, "end of life on "+it.EOL)
		}
	}
	if it.Jump != "" {
		why = append(why, it.Jump)
	}
	if it.Behind != "" {
		why = append(why, "behind "+it.Behind)
	}
	if len(why) > 0 {
		b.WriteString(" (" + strings.Join(why, ", ") + ")")
	}
	if it.TargetURL != "" {
		b.WriteString(" — " + it.TargetURL)
	}
	return b.String() + "\n"
}

// nextVersion is the smallest version after v, its last number plus one ("26.8.0" ->
// "26.8.1"): an acknowledgement until it holds while nothing newer than v is out.
func nextVersion(v string) string {
	if _, ok := versions.ParseVersion(v); !ok {
		return ""
	}
	end := strings.IndexFunc(v, func(r rune) bool { return r == '-' || r == '+' || r == '_' })
	if end < 0 {
		end = len(v)
	}
	start := end
	for start > 0 && v[start-1] >= '0' && v[start-1] <= '9' {
		start--
	}
	if start == end {
		return ""
	}
	n, err := strconv.Atoi(v[start:end])
	if err != nil {
		return ""
	}
	return v[:start] + strconv.Itoa(n+1) + v[end:]
}

// updatesURL keeps the page's filters, changing one.
func updatesURL(v UpdatesView, key, value string) string {
	q := url.Values{}
	if v.Team != "" {
		q.Set("team", v.Team)
	}
	if v.Env != "" {
		q.Set("env", v.Env)
	}
	if v.Level >= 0 {
		q.Set("level", itoa(v.Level))
	}
	if value == "" {
		q.Del(key)
	} else {
		q.Set(key, value)
	}
	if len(q) == 0 {
		return "/updates"
	}
	return "/updates?" + q.Encode()
}

func sortedSet(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
