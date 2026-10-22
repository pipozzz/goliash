// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package notifier

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/pipozzz/goliash/internal/store"
	"github.com/pipozzz/goliash/internal/versions"
)

// The security posture report: rules with the event type posture_report get the Security page in one message
// at the plan's time (daily, or on Mondays): how long production runs behind available releases, end of life,
// accepted risk and the longest exposures. It covers production, so the rule's filters do not apply.

// EventPostureReport is the rule event type of the security posture report.
const EventPostureReport = "posture_report"

// postureWorst is how many of the longest production exposures the report names.
const postureWorst = 3

// ErrNotAReportRule means a rule does not send the posture report.
var ErrNotAReportRule = errors.New("this rule does not send the security posture report")

func (n *Notifier) reportPosture(ctx context.Context, sc store.Scope, rules []store.Rule, now time.Time) error {
	it, err := n.postureItem(ctx, sc, now)
	if err != nil {
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
			Scope: sc, RuleID: r.ID, Payload: payload, DueAt: now, DedupKey: "posture|" + period,
		}); err != nil {
			return err
		}
	}
	return nil
}

// RecordPostures stores today's posture of every workspace, for its trend.
func (n *Notifier) RecordPostures(ctx context.Context) error {
	wss, err := n.store.ListWorkspaces(ctx)
	if err != nil {
		return err
	}
	now := n.now()
	for _, ws := range wss {
		sc := ws.Scope()
		o, err := versions.LoadOverview(ctx, n.store, sc)
		if err != nil {
			return err
		}
		pos, err := versions.LoadPosture(ctx, n.store, sc, o, now)
		if err != nil {
			return err
		}
		if err := n.store.RecordPosture(ctx, sc, pos.Sample(now)); err != nil {
			return err
		}
	}
	return nil
}

// SendPostureNow sends a rule's posture report at once, to check the channel without waiting.
func (n *Notifier) SendPostureNow(ctx context.Context, sc store.Scope, ruleID string) error {
	targets, err := n.ruleTargets(ctx, sc)
	if err != nil {
		return err
	}
	rt, ok := targets[ruleID]
	if !ok {
		return store.ErrNotFound
	}
	if !contains(rt.rule.EventTypes, EventPostureReport) {
		return ErrNotAReportRule
	}
	it, err := n.postureItem(ctx, sc, n.now())
	if err != nil {
		return err
	}
	return n.sendNow(ctx, sc, rt, Message{Workspace: rt.workspace, Items: []Item{it}, Link: n.link, Goli: n.goli})
}

func (n *Notifier) postureItem(ctx context.Context, sc store.Scope, now time.Time) (Item, error) {
	o, err := versions.LoadOverview(ctx, n.store, sc)
	if err != nil {
		return Item{}, err
	}
	pos, err := versions.LoadPosture(ctx, n.store, sc, o, now)
	if err != nil {
		return Item{}, err
	}
	var supply SupplyChain
	if pos.Prod.ID != "" {
		if supply.Vulns, err = versions.LoadVulnReport(ctx, n.store, sc, pos.Prod.ID); err != nil {
			return Item{}, err
		}
		if supply.Licenses, err = versions.LoadLicenseReport(ctx, n.store, sc, pos.Prod.ID); err != nil {
			return Item{}, err
		}
	}
	if apps, err := versions.GitOpsApps(ctx, n.store, sc, o); err == nil && len(apps) > 0 {
		if supply.Pulls, err = versions.PullTimings(ctx, n.store, sc, apps, now.Add(-7*24*time.Hour)); err != nil {
			return Item{}, err
		}
	}
	it := PostureItem(pos, supply, n.weekAgo(ctx, sc, now), now)
	if acks, err := n.store.ListAcks(ctx, sc); err == nil {
		if s := overdueLine(versions.LoadUpdates(ctx, n.store, sc, o, acks, now), now); s != "" {
			it.Text += " " + s
		}
	}
	return it, nil
}

// SupplyChain is what the SBOMs of production's images say, for the posture report: known vulnerabilities and
// licenses that need a look; and how the week's upgrades went out (the pull requests Goliash opened in it). Zero
// values leave them out.
type SupplyChain struct {
	Vulns    versions.VulnReport
	Licenses versions.LicenseReport
	Pulls    []versions.PullTiming
}

// supplyChainText says what the SBOMs of production's images say: known vulnerabilities, exploited ones first, and
// application packages whose licenses need a look. Empty when no image has an SBOM.
func supplyChainText(s SupplyChain) string {
	var b strings.Builder
	if s.Vulns.WithSBOM > 0 {
		fmt.Fprintf(&b, " Known vulnerabilities in %s with an SBOM: %d", plural(s.Vulns.WithSBOM, "image", "images"), len(s.Vulns.Findings))
		if s.Vulns.Exploited > 0 {
			fmt.Fprintf(&b, ", %d exploited in the wild", s.Vulns.Exploited)
			var ids []string
			for _, f := range s.Vulns.Findings {
				if f.Exploited != nil && len(ids) < postureWorst {
					ids = append(ids, f.Key)
				}
			}
			b.WriteString(" (" + strings.Join(ids, ", ") + ")")
		}
		fmt.Fprintf(&b, "; %d with a fix out.", s.Vulns.Fixable)
	}
	if s.Licenses.WithSBOM > 0 {
		var parts []string
		for _, c := range []struct {
			class versions.LicenseClass
			name  string
		}{{versions.NetworkCopyleft, "network copyleft"}, {versions.SourceAvailable, "source-available"}, {versions.StrongCopyleft, "strong copyleft"}, {versions.UnknownLicense, "unknown"}} {
			if n := s.Licenses.Counts[c.class]; n > 0 {
				parts = append(parts, fmt.Sprintf("%d %s", n, c.name))
			}
		}
		if len(parts) > 0 {
			b.WriteString(" Licenses that need a look: " + strings.Join(parts, ", ") + ".")
		}
	}
	return b.String()
}

// weekAgo is the posture a week before now, or nil when there is no reading from 7 to 14 days back.
func (n *Notifier) weekAgo(ctx context.Context, sc store.Scope, now time.Time) *store.PostureSample {
	hist, err := n.store.PostureHistory(ctx, sc, now.AddDate(0, 0, -14).UTC().Format(time.DateOnly))
	if err != nil {
		return nil
	}
	cutoff := now.AddDate(0, 0, -7).UTC().Format(time.DateOnly)
	var prev *store.PostureSample
	for i := range hist {
		if hist[i].Day <= cutoff {
			prev = &hist[i]
		}
	}
	return prev
}

// PostureItem is the posture as a notification item: the numbers a security officer follows week to week, the
// longest production exposures, and the same numbers a week ago when known.
func PostureItem(pos versions.Posture, supply SupplyChain, prev *store.PostureSample, now time.Time) Item {
	prod := pos.Prod.Name
	var b strings.Builder
	fmt.Fprintf(&b, "%s runs %s", orDash(prod), plural(pos.ProdRunning, "service", "services"))
	exposed := 0
	var worst []string
	for _, e := range pos.Exposures {
		if e.Environment.ID != pos.Prod.ID || e.Acked {
			continue
		}
		exposed++
		if len(worst) < postureWorst {
			days := plural(max(e.Days, 0), "day", "days")
			if !e.Known {
				days = "≥ " + days
			}
			to := e.Target
			if to == "" {
				to = "a supported release"
			}
			name := e.Service.Name
			if e.App != "" {
				name += " (" + e.App + ")"
			}
			worst = append(worst, fmt.Sprintf("%s %s → %s (%s)", name, e.Running, to, days))
		}
	}
	if exposed == 0 {
		b.WriteString(", none behind a newer release")
	} else {
		fmt.Fprintf(&b, ", %d behind a newer release: median %d days, 90th percentile %d; %d behind 30+ days, %d behind 90+",
			exposed, pos.ProdMedian, pos.ProdP90, pos.Prod30, pos.Prod90)
	}
	fmt.Fprintf(&b, ". End of life: %d passed, %d soon. Accepted risk: %d.", pos.EOLPassed, pos.EOLSoon, pos.Accepted)
	if len(pos.Stale) > 0 || len(pos.NoUpstream) > 0 {
		fmt.Fprintf(&b, " Blind spots: %d stale targets, %d services without a known upstream%s.", len(pos.Stale), len(pos.NoUpstream), unknownReasons(pos.Unknowns))
	}
	if n := len(pos.OutdatedBase); n > 0 {
		names := pos.OutdatedBase
		if n > postureWorst {
			names = append(append([]string{}, names[:postureWorst]...), fmt.Sprintf("%d more", n-postureWorst))
		}
		fmt.Fprintf(&b, " Built on an outdated base (rebuild with --pull): %s.", strings.Join(names, ", "))
	}
	if len(worst) > 0 {
		b.WriteString(" Longest: " + strings.Join(worst, "; ") + ".")
	}
	b.WriteString(supplyChainText(supply))
	b.WriteString(pullsText(supply.Pulls, now))
	if prev != nil {
		fmt.Fprintf(&b, " A week ago (%s): %d behind, median %d days, %d behind 90+, %d past end of life.",
			prev.Day, prev.Exposed, prev.Median, prev.Behind90, prev.EOLPassed)
	}
	// No environment on the item: the text names production, and channels show it whole.
	return Item{Type: "posture", At: now, Text: b.String()}
}

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}

// pullsText says how the week's upgrades went out: the pull requests Goliash opened, merged and running, the median
// from merge to running, and those merged whose version does not run yet, with why. Empty without any.
func pullsText(pulls []versions.PullTiming, now time.Time) string {
	if len(pulls) == 0 {
		return ""
	}
	merged, running := 0, 0
	var toRunning []time.Duration
	var waiting []string
	for _, p := range pulls {
		if p.State == "merged" {
			merged++
		}
		if p.Deployed {
			running++
			toRunning = append(toRunning, p.MergeToRunning)
		}
		if p.Undeployed() && len(waiting) < postureWorst {
			w := p.App.Service + " " + p.To + " in " + p.Environment
			if why := versions.WhyWaiting(p.App, now); why != "" {
				w += " (" + why + ")"
			}
			waiting = append(waiting, w)
		}
	}
	s := fmt.Sprintf(" Upgrades as pull requests this week: %d opened, %d merged, %d running", len(pulls), merged, running)
	if d, n := versions.MedianDuration(toRunning); n > 0 {
		s += ", median " + versions.HumanDuration(d) + " from merge to running"
	}
	s += "."
	if len(waiting) > 0 {
		s += " Merged, not deployed: " + strings.Join(waiting, "; ") + "."
	}
	return s
}

// unknownReasons counts why the newest releases are unknown, most common first: " (3 not a version, 1 check failed)";
// empty without any.
func unknownReasons(us []versions.UnknownService) string {
	count := map[string]int{}
	var order []string
	for _, u := range us {
		if count[u.Short] == 0 {
			order = append(order, u.Short)
		}
		count[u.Short]++
	}
	if len(order) == 0 {
		return ""
	}
	sort.SliceStable(order, func(i, j int) bool { return count[order[i]] > count[order[j]] })
	parts := make([]string, len(order))
	for i, s := range order {
		parts[i] = fmt.Sprintf("%d %s", count[s], s)
	}
	return " (" + strings.Join(parts, ", ") + ")"
}
