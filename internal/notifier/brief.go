// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package notifier

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/pipozzz/goliash/internal/store"
	"github.com/pipozzz/goliash/internal/versions"
)

// EventSecurityBrief sends, daily or on Mondays, what a security team should look at today: new releases that fix
// vulnerabilities in what runs, security watches, exploited vulnerabilities (past CISA's due date first), merged
// fixes not deployed and the security upgrades waiting. Nothing to say, nothing sent.
const EventSecurityBrief = "security_brief"

// briefShown bounds how many items of a kind the brief names.
const briefShown = 3

// briefWorkspace queues the brief for every due rule, once per period.
func (n *Notifier) briefWorkspace(ctx context.Context, sc store.Scope, rules []store.Rule, now time.Time) error {
	it, worth, err := n.briefItem(ctx, sc, now)
	if err != nil || !worth {
		return err
	}
	payload, err := json.Marshal(it)
	if err != nil {
		return err
	}
	for _, r := range rules {
		var f Filter
		_ = json.Unmarshal(r.Filter, &f)
		period := lastPlanTime(r.Mode, f, now).Format("2006-01-02")
		if _, err := n.store.Enqueue(ctx, store.QueueItem{
			Scope: sc, RuleID: r.ID, Payload: payload, DueAt: now, DedupKey: "brief|" + period,
		}); err != nil {
			return err
		}
	}
	return nil
}

// Brief is what a security team should look at today, for the brief and the Security page's Since yesterday.
type Brief struct {
	Fixes      []BriefFix            // releases found in the last day that fix vulnerabilities in what runs
	NewKEV     []string              // vulnerabilities that run and CISA listed as exploited since yesterday
	Watches    []BriefWatch          // the security watches, with how many services each covers
	Exploited  int                   // vulnerabilities exploited in the wild that run
	PastDue    []string              // of them, those past CISA's due date
	Waiting    int                   // upgrades fixing vulnerabilities not acknowledged
	Overdue    []versions.Update     // upgrades past the workspace's remediation deadline, longest past first
	Undeployed []versions.PullTiming // pull requests merged in the last 30 days whose version does not run
}

// BriefFix is a new release that fixes vulnerabilities in a service, and what runs.
type BriefFix struct{ Service, Version, Running string }

// BriefWatch is a security watch and how many services it covers.
type BriefWatch struct {
	Term     string
	Services int
}

// Any reports whether the brief has something to look at; watches alone do not count.
func (b Brief) Any() bool {
	return len(b.Fixes) > 0 || len(b.NewKEV) > 0 || b.Exploited > 0 || b.Waiting > 0 || len(b.Overdue) > 0 || len(b.Undeployed) > 0
}

// LoadBrief gathers the brief of a workspace at now.
func LoadBrief(ctx context.Context, st *store.Store, sc store.Scope, now time.Time) (Brief, error) {
	var b Brief
	o, err := versions.LoadOverview(ctx, st, sc)
	if err != nil {
		return b, err
	}
	// New releases that fix vulnerabilities in what runs: discovered in the last day, newer than what runs.
	releases, err := st.ReleasesByService(ctx, sc)
	if err != nil {
		return b, err
	}
	for id, rs := range releases {
		ref, runs := o.Refs[id]
		cur, ok := versions.ParseVersion(ref.Tag)
		if !runs || !ok {
			continue
		}
		var newest string
		var newestV versions.Version
		for _, r := range rs {
			v, ok := versions.ParseVersion(r.Version)
			if r.Security == "" || !ok || v.Compare(cur) <= 0 || now.Sub(r.DiscoveredAt) > 24*time.Hour {
				continue
			}
			if newest == "" || v.Compare(newestV) > 0 {
				newest, newestV = r.Version, v
			}
		}
		if newest != "" {
			b.Fixes = append(b.Fixes, BriefFix{Service: o.Services[id].Name, Version: newest, Running: ref.Tag})
		}
	}
	sort.Slice(b.Fixes, func(i, j int) bool { return b.Fixes[i].Service < b.Fixes[j].Service })

	// Security watches, and what they cover.
	if ws, err := st.ListWatches(ctx, sc, now); err == nil && len(ws) > 0 {
		terms := make([]string, len(ws))
		for i, w := range ws {
			terms[i] = w.Term
		}
		if matches, err := versions.WatchMatches(ctx, st, sc, o, terms); err == nil {
			for _, w := range ws {
				b.Watches = append(b.Watches, BriefWatch{Term: w.Term, Services: len(matches[w.Term])})
			}
		}
	}

	// Exploited vulnerabilities: those CISA listed since yesterday, and those past its due date.
	if rep, err := versions.LoadVulnReport(ctx, st, sc, ""); err == nil {
		b.Exploited = rep.Exploited
		yesterday := now.UTC().AddDate(0, 0, -1).Format(time.DateOnly)
		for _, f := range rep.Findings {
			if f.Exploited != nil && f.Exploited.DateAdded >= yesterday {
				b.NewKEV = append(b.NewKEV, f.Key)
			}
			if f.PastDue(now) {
				b.PastDue = append(b.PastDue, f.Key)
			}
		}
	}

	// Security upgrades waiting, those past their deadline, and merged fixes not deployed.
	acks, _ := st.ListAcks(ctx, sc)
	us := versions.LoadUpdates(ctx, st, sc, o, acks, now)
	for _, u := range us {
		if u.Acked {
			continue
		}
		if u.Urgency == versions.UrgencyExploited || u.Urgency == versions.UrgencySecurity {
			b.Waiting++
		}
		if u.Overdue(now) {
			b.Overdue = append(b.Overdue, u)
		}
	}
	sort.SliceStable(b.Overdue, func(i, j int) bool { return b.Overdue[i].Due.Before(b.Overdue[j].Due) })
	if apps, err := versions.GitOpsApps(ctx, st, sc, o); err == nil && len(apps) > 0 {
		if pulls, err := versions.PullTimings(ctx, st, sc, apps, now.AddDate(0, 0, -30)); err == nil {
			for _, p := range pulls {
				if p.Undeployed() {
					b.Undeployed = append(b.Undeployed, p)
				}
			}
		}
	}
	return b, nil
}

// Text is the brief in a few sentences.
func (b Brief) Text(now time.Time) string {
	var parts []string
	if len(b.Fixes) > 0 {
		fixes := make([]string, len(b.Fixes))
		for i, f := range b.Fixes {
			fixes[i] = fmt.Sprintf("%s %s (%s runs)", f.Service, f.Version, f.Running)
		}
		parts = append(parts, fmt.Sprintf("%s since yesterday: %s.", plural(len(fixes), "security release", "security releases"), named(fixes)))
	}
	if len(b.NewKEV) > 0 {
		parts = append(parts, fmt.Sprintf("Newly listed by CISA as exploited in the wild, and running: %s.", named(b.NewKEV)))
	}
	if len(b.Watches) > 0 {
		watching := make([]string, len(b.Watches))
		for i, w := range b.Watches {
			watching[i] = fmt.Sprintf("“%s” (%s)", w.Term, plural(w.Services, "service", "services"))
		}
		parts = append(parts, "Watching "+strings.Join(watching, ", ")+".")
	}
	if b.Exploited > 0 {
		s := fmt.Sprintf("%s exploited in the wild run", plural(b.Exploited, "vulnerability", "vulnerabilities"))
		if len(b.PastDue) > 0 {
			s += fmt.Sprintf(", %d past CISA's due date (%s)", len(b.PastDue), named(b.PastDue))
		}
		parts = append(parts, s+".")
	}
	if b.Waiting > 0 {
		parts = append(parts, fmt.Sprintf("%s fixing vulnerabilities wait in Updates.", plural(b.Waiting, "upgrade", "upgrades")))
	}
	if s := overdueLine(b.Overdue, now); s != "" {
		parts = append(parts, s)
	}
	if len(b.Undeployed) > 0 {
		parts = append(parts, fmt.Sprintf("%s merged and not deployed.", plural(len(b.Undeployed), "pull request", "pull requests")))
	}
	if !b.Any() {
		return "Nothing new to look at: no security release since yesterday, nothing exploited running, no security upgrade waiting."
	}
	return strings.Join(parts, " ")
}

// briefItem is the brief as one item, and whether it has anything to say.
func (n *Notifier) briefItem(ctx context.Context, sc store.Scope, now time.Time) (Item, bool, error) {
	b, err := LoadBrief(ctx, n.store, sc, now)
	if err != nil {
		return Item{}, false, err
	}
	return Item{Type: EventSecurityBrief, At: now, URL: "/security", Text: b.Text(now)}, b.Any(), nil
}

// named lists the first briefShown, and how many more.
func named(list []string) string {
	if len(list) <= briefShown {
		return strings.Join(list, "; ")
	}
	return strings.Join(list[:briefShown], "; ") + fmt.Sprintf("; %d more", len(list)-briefShown)
}

// ErrNotABriefRule means a rule does not send the security brief.
var ErrNotABriefRule = fmt.Errorf("this rule does not send the security brief")

// SendBriefNow sends a rule's security brief at once, to check the channel; it says so when there is nothing new.
func (n *Notifier) SendBriefNow(ctx context.Context, sc store.Scope, ruleID string) error {
	targets, err := n.ruleTargets(ctx, sc)
	if err != nil {
		return err
	}
	rt, ok := targets[ruleID]
	if !ok {
		return store.ErrNotFound
	}
	if !contains(rt.rule.EventTypes, EventSecurityBrief) {
		return ErrNotABriefRule
	}
	it, _, err := n.briefItem(ctx, sc, n.now())
	if err != nil {
		return err
	}
	return n.sendNow(ctx, sc, rt, Message{Workspace: rt.workspace, Items: []Item{it}, Link: n.link, Goli: n.goli})
}

// overdueLine names the open upgrades past the workspace's remediation deadline, longest past first: "2 upgrades
// past their remediation deadline: php in prod (exploited vulnerability, 12 days over); …". "" when none is.
func overdueLine(us []versions.Update, now time.Time) string {
	var over []versions.Update
	for _, u := range us {
		if !u.Acked && u.Overdue(now) {
			over = append(over, u)
		}
	}
	if len(over) == 0 {
		return ""
	}
	sort.SliceStable(over, func(i, j int) bool { return over[i].Due.Before(over[j].Due) })
	names := make([]string, len(over))
	for i, u := range over {
		names[i] = fmt.Sprintf("%s in %s (%s, %s over)", u.Service.Name, u.Environment.Name, u.DueFor, plural(-u.DaysLeft(now), "day", "days"))
	}
	return fmt.Sprintf("%s past their remediation deadline: %s.", plural(len(over), "upgrade", "upgrades"), named(names))
}
