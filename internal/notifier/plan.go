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
	hour := 8
	if f.DigestHour != nil && *f.DigestHour >= 0 && *f.DigestHour < 24 {
		hour = *f.DigestHour
	}
	t := time.Date(now.Year(), now.Month(), now.Day(), hour, 0, 0, 0, time.UTC)
	if mode == "daily" {
		if t.After(now) {
			t = t.AddDate(0, 0, -1)
		}
		return t
	}
	for t.Weekday() != time.Monday || t.After(now) {
		t = t.AddDate(0, 0, -1)
	}
	return t
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
		var due []store.Rule
		for _, r := range rules {
			if !contains(r.EventTypes, EventUpdatesPlan) {
				continue
			}
			var f Filter
			if json.Unmarshal(r.Filter, &f) != nil {
				continue
			}
			if at := lastPlanTime(r.Mode, f, now); now.Sub(at) < planWindow {
				due = append(due, r)
			}
		}
		if len(due) == 0 {
			continue
		}
		if err := n.planWorkspace(ctx, sc, due, now); err != nil {
			return err
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
	updates := versions.Updates(o, acks, now)
	for _, r := range rules {
		var f Filter
		_ = json.Unmarshal(r.Filter, &f)
		period := lastPlanTime(r.Mode, f, now).Format("2006-01-02")
		for _, u := range updates {
			if u.Acked {
				continue
			}
			it := planItem(u, now)
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
	msg := Message{Workspace: rt.workspace, Digest: true, Link: n.link}
	for _, u := range versions.Updates(o, acks, now) {
		if u.Acked {
			continue
		}
		it := planItem(u, now)
		if matches(store.Rule{EventTypes: []string{"update"}}, f, it) {
			msg.Items = append(msg.Items, it)
		}
	}
	if len(msg.Items) == 0 {
		return 0, nil
	}
	sender, ok := n.senders[rt.channel.Type]
	if !ok {
		return 0, fmt.Errorf("no sender for channel type %q", rt.channel.Type)
	}
	sendCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	return len(msg.Items), sender.Send(sendCtx, rt.channel, msg)
}

// planItem is one update as a notification item.
func planItem(u versions.Update, now time.Time) Item {
	it := Item{
		Type: "update", Service: u.Service.Name, App: u.App, Owner: u.Service.Owner, Environment: u.Environment.Name,
		From: u.Running, To: u.Target, Note: versions.UrgencyLabels[u.Urgency], URL: u.TargetURL, At: now,
	}
	it.Text = Describe(it)
	return it
}
