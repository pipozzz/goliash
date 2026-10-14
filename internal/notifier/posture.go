// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package notifier

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	return PostureItem(pos, now), nil
}

// PostureItem is the posture as a notification item: the numbers a security officer follows week to week, and
// the longest production exposures.
func PostureItem(pos versions.Posture, now time.Time) Item {
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
		fmt.Fprintf(&b, " Blind spots: %d stale targets, %d services without a known upstream.", len(pos.Stale), len(pos.NoUpstream))
	}
	if len(worst) > 0 {
		b.WriteString(" Longest: " + strings.Join(worst, "; ") + ".")
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
