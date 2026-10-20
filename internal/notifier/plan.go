// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package notifier

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/pipozzz/goliash/internal/store"
	"github.com/pipozzz/goliash/internal/versions"
)

// The upgrade plan: rules with the event type updates_plan get the Updates list
// (what to upgrade, most urgent first) at their digest time, daily or on Mondays
// (weekly for an instant rule too), as one message. Updates an acknowledgement puts
// off are left out; the rule's filters apply.

// EventUpdatesPlan is the rule event type of the upgrade plan.
const EventUpdatesPlan = "updates_plan"

// planWindow is how long after its time a plan may still go out, e.g. after a restart.
const planWindow = 6 * time.Hour

// lastPlanTime is the most recent time a rule's plan was due: its digest hour today
// (daily) or on Monday (weekly, and instant).
func lastPlanTime(mode string, f Filter, now time.Time) time.Time {
	loc := f.Location()
	local := now.In(loc)
	t := time.Date(local.Year(), local.Month(), local.Day(), f.digestHour(), 0, 0, 0, loc)
	if mode == "daily" {
		if t.After(now) {
			t = t.AddDate(0, 0, -1)
		}
		return t.UTC()
	}
	for t.Weekday() != time.Monday || t.After(now) {
		t = t.AddDate(0, 0, -1)
	}
	return t.UTC()
}

// PlanUpdates queues the upgrade plan of every rule whose plan is due, once per
// period (the queue's dedup key holds the period).
func (n *Notifier) PlanUpdates(ctx context.Context) error {
	wss, err := n.store.ListWorkspaces(ctx)
	if err != nil {
		return err
	}
	now := n.now()
	for _, ws := range wss {
		sc := ws.Scope()
		rules, err := n.store.ActiveRules(ctx, sc)
		if err != nil {
			return err
		}
		var due, reports []store.Rule
		for _, r := range rules {
			plan, report := contains(r.EventTypes, EventUpdatesPlan), contains(r.EventTypes, EventPostureReport)
			if !plan && !report {
				continue
			}
			var f Filter
			if json.Unmarshal(r.Filter, &f) != nil {
				continue
			}
			if at := lastPlanTime(r.Mode, f, now); now.Sub(at) < planWindow {
				if plan {
					due = append(due, r)
				}
				if report {
					reports = append(reports, r)
				}
			}
		}
		if len(due) > 0 {
			if err := n.planWorkspace(ctx, sc, due, now); err != nil {
				return err
			}
		}
		if len(reports) > 0 {
			if err := n.reportPosture(ctx, sc, reports, now); err != nil {
				return err
			}
		}
	}
	return nil
}

func (n *Notifier) planWorkspace(ctx context.Context, sc store.Scope, rules []store.Rule, now time.Time) error {
	o, err := versions.LoadOverview(ctx, n.store, sc)
	if err != nil {
		return err
	}
	acks, err := n.store.ListAcks(ctx, sc)
	if err != nil {
		return err
	}
	updates := versions.LoadUpdates(ctx, n.store, sc, o, acks, now)
	apps, err := versions.ServiceApps(ctx, n.store, sc)
	if err != nil {
		return err
	}
	offer := func(string, string, string, string) string { return "" }
	if n.planPRs != nil {
		offer = n.planPRs(ctx, sc)
	}
	for _, r := range rules {
		var f Filter
		_ = json.Unmarshal(r.Filter, &f)
		period := lastPlanTime(r.Mode, f, now).Format("2006-01-02")
		for _, u := range updates {
			if u.Acked {
				continue
			}
			it := planItem(u, now, apps)
			if p := offer(u.Service.ID, u.Environment.Name, u.Running, u.Target); p != "" { // a click from the plan to the fix
				it.Action, it.ActionPath = "Open PR → "+u.Target, p
			}
			if !matches(store.Rule{EventTypes: []string{"update"}}, f, it) {
				continue
			}
			payload, err := json.Marshal(it)
			if err != nil {
				return err
			}
			if _, err := n.store.Enqueue(ctx, store.QueueItem{
				Scope: sc, RuleID: r.ID, Payload: payload, DueAt: now,
				DedupKey: "plan|" + period + "|" + u.Service.ID + "|" + u.App + "|" + u.Environment.ID,
			}); err != nil {
				return err
			}
		}
	}
	return nil
}

// ErrNotAPlanRule means a rule does not send the upgrade plan.
var ErrNotAPlanRule = errors.New("this rule does not send the upgrade plan")

// SendPlanNow sends a rule's upgrade plan at once, to check a channel and its filters
// without waiting for Monday. It returns how many updates the plan had; nothing is
// sent when there are none. Scheduled plans are not affected.
func (n *Notifier) SendPlanNow(ctx context.Context, sc store.Scope, ruleID string) (int, error) {
	targets, err := n.ruleTargets(ctx, sc)
	if err != nil {
		return 0, err
	}
	rt, ok := targets[ruleID]
	if !ok {
		return 0, store.ErrNotFound
	}
	if !contains(rt.rule.EventTypes, EventUpdatesPlan) {
		return 0, ErrNotAPlanRule
	}
	var f Filter
	_ = json.Unmarshal(rt.rule.Filter, &f)
	o, err := versions.LoadOverview(ctx, n.store, sc)
	if err != nil {
		return 0, err
	}
	acks, err := n.store.ListAcks(ctx, sc)
	if err != nil {
		return 0, err
	}
	now := n.now()
	apps, err := versions.ServiceApps(ctx, n.store, sc)
	if err != nil {
		return 0, err
	}
	msg := Message{Workspace: rt.workspace, Digest: true, Link: n.link}
	for _, u := range versions.LoadUpdates(ctx, n.store, sc, o, acks, now) {
		if u.Acked {
			continue
		}
		it := planItem(u, now, apps)
		if matches(store.Rule{EventTypes: []string{"update"}}, f, it) {
			msg.Items = append(msg.Items, it)
		}
	}
	if len(msg.Items) == 0 {
		return 0, nil
	}
	n.addTake(ctx, &msg)
	if err := n.sendNow(ctx, sc, rt, msg); err != nil {
		return 0, err
	}
	return len(msg.Items), nil
}

// sendNow sends a message through a rule's channel at once, and records it as sent so it shows
// under the bell and in the delivery log.
func (n *Notifier) sendNow(ctx context.Context, sc store.Scope, rt ruleTarget, msg Message) error {
	sender, ok := n.senders[rt.channel.Type]
	if !ok {
		return fmt.Errorf("no sender for channel type %q", rt.channel.Type)
	}
	sendCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := sender.Send(sendCtx, rt.channel, msg); err != nil {
		return err
	}
	payloads := make([]json.RawMessage, 0, len(msg.Items))
	for _, it := range msg.Items {
		p, err := json.Marshal(it)
		if err != nil {
			return err
		}
		payloads = append(payloads, p)
	}
	if err := n.store.RecordSent(ctx, sc, rt.rule.ID, payloads); err != nil {
		return err
	}
	if n.onSent != nil {
		n.onSent(sc)
	}
	return nil
}

// planItem is one update as a notification item.
func planItem(u versions.Update, now time.Time, apps map[string][]string) Item {
	it := Item{
		Type: "update", Service: u.Service.Name, App: u.App, Apps: itemApps(u.App, apps[u.Service.ID]), Owner: u.Service.Owner, Environment: u.Environment.Name,
		From: u.Running, To: u.Target, Note: versions.UrgencyLabels[u.Urgency], URL: u.TargetURL, At: now,
	}
	it.Text = Describe(it)
	if n := len(u.Vulns.Fixed); n > 0 {
		it.Text += "; fixes " + plural(n, "known vulnerability", "known vulnerabilities")
		if x := versions.Exploited(u.Vulns.Fixed); x > 0 {
			it.Text += fmt.Sprintf(", %d exploited in the wild", x)
		}
	}
	return it
}

// SetPlanPRs lets the upgrade plan link, per upgrade, the preview of the pull request Goliash would open for it.
func (n *Notifier) SetPlanPRs(planPRs func(ctx context.Context, sc store.Scope) func(serviceID, env, running, target string) string) {
	n.planPRs = planPRs
}
