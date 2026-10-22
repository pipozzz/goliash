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

// briefItem is the brief as one item, and whether it has anything to say.
func (n *Notifier) briefItem(ctx context.Context, sc store.Scope, now time.Time) (Item, bool, error) {
	o, err := versions.LoadOverview(ctx, n.store, sc)
	if err != nil {
		return Item{}, false, err
	}
	var parts []string

	// New releases that fix vulnerabilities in what runs: discovered in the last day, newer than what runs.
	releases, err := n.store.ReleasesByService(ctx, sc)
	if err != nil {
		return Item{}, false, err
	}
	var fixes []string
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
			fixes = append(fixes, fmt.Sprintf("%s %s (%s runs)", o.Services[id].Name, newest, ref.Tag))
		}
	}
	sort.Strings(fixes)
	if len(fixes) > 0 {
		parts = append(parts, fmt.Sprintf("%s since yesterday: %s.", plural(len(fixes), "security release", "security releases"), named(fixes)))
	}

	// Security watches, and what they cover.
	if ws, err := n.store.ListWatches(ctx, sc, now); err == nil && len(ws) > 0 {
		terms := make([]string, len(ws))
		for i, w := range ws {
			terms[i] = w.Term
		}
		if matches, err := versions.WatchMatches(ctx, n.store, sc, o, terms); err == nil {
			var watching []string
			for _, w := range ws {
				watching = append(watching, fmt.Sprintf("“%s” (%s)", w.Term, plural(len(matches[w.Term]), "service", "services")))
			}
			parts = append(parts, "Watching "+strings.Join(watching, ", ")+".")
		}
	}

	// Exploited vulnerabilities, those past CISA's due date first.
	if rep, err := versions.LoadVulnReport(ctx, n.store, sc, ""); err == nil && rep.Exploited > 0 {
		var due []string
		for _, f := range rep.Findings {
			if f.PastDue(now) {
				due = append(due, f.Key)
			}
		}
		s := fmt.Sprintf("%s exploited in the wild run", plural(rep.Exploited, "vulnerability", "vulnerabilities"))
		if len(due) > 0 {
			s += fmt.Sprintf(", %d past CISA's due date (%s)", len(due), named(due))
		}
		parts = append(parts, s+".")
	}

	// Security upgrades waiting, and merged fixes not deployed.
	acks, _ := n.store.ListAcks(ctx, sc)
	waiting := 0
	us := versions.LoadUpdates(ctx, n.store, sc, o, acks, now)
	for _, u := range us {
		if !u.Acked && (u.Urgency == versions.UrgencyExploited || u.Urgency == versions.UrgencySecurity) {
			waiting++
		}
	}
	if waiting > 0 {
		parts = append(parts, fmt.Sprintf("%s fixing vulnerabilities wait in Updates.", plural(waiting, "upgrade", "upgrades")))
	}
	if s := overdueLine(us, now); s != "" {
		parts = append(parts, s)
	}
	if apps, err := versions.GitOpsApps(ctx, n.store, sc, o); err == nil && len(apps) > 0 {
		if pulls, err := versions.PullTimings(ctx, n.store, sc, apps, now.AddDate(0, 0, -30)); err == nil {
			undeployed := 0
			for _, p := range pulls {
				if p.Undeployed() {
					undeployed++
				}
			}
			if undeployed > 0 {
				parts = append(parts, fmt.Sprintf("%s merged and not deployed.", plural(undeployed, "pull request", "pull requests")))
			}
		}
	}
	it := Item{Type: EventSecurityBrief, At: now, URL: "/security"}
	if len(parts) == 0 {
		it.Text = "Nothing new to look at: no security release since yesterday, nothing exploited running, no security upgrade waiting."
		return it, false, nil
	}
	it.Text = strings.Join(parts, " ")
	return it, true, nil
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
