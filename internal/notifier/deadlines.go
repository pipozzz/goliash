// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package notifier

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/pipozzz/goliash/internal/store"
	"github.com/pipozzz/goliash/internal/versions"
)

// EventDeadline announces an upgrade nearing or past the remediation deadline the workspace set (Security page):
// three days before it, once, and the day it passes, once. A rule filtered by owner sends a team its own. It also
// asks admins to approve an accepted risk that waits for them, and says three days ahead when one ends.
const EventDeadline = "deadline"

const (
	deadlineWarnDays   = 3 // how many days before the deadline the warning goes
	deadlineRecentDays = 7 // a deadline passed longer ago is not announced (the brief and report name it)
)

// CheckDeadlines announces the upgrades that near or passed their remediation deadline, in every workspace with
// deadlines set, and the accepted risks waiting for approval or about to end.
func (n *Notifier) CheckDeadlines(ctx context.Context) error {
	wss, err := n.store.ListWorkspaces(ctx)
	if err != nil {
		return err
	}
	now := n.now()
	for _, ws := range wss {
		sc := ws.Scope()
		rem, err := n.store.Remediation(ctx, sc)
		if err != nil {
			continue
		}
		rules, err := n.store.ActiveRules(ctx, sc)
		if err != nil {
			return err
		}
		if !anyRuleFor(rules, EventDeadline) {
			continue
		}
		if rem.Any() {
			if err := n.deadlineAlerts(ctx, sc, rules, now); err != nil {
				return err
			}
		}
		if err := n.riskAlerts(ctx, sc, rules, now); err != nil {
			return err
		}
	}
	return nil
}

// anyRuleFor reports whether a rule sends an event type: by name, or every type.
func anyRuleFor(rules []store.Rule, event string) bool {
	for _, r := range rules {
		if len(r.EventTypes) == 0 || contains(r.EventTypes, event) {
			return true
		}
	}
	return false
}

func (n *Notifier) deadlineAlerts(ctx context.Context, sc store.Scope, rules []store.Rule, now time.Time) error {
	o, err := versions.LoadOverview(ctx, n.store, sc)
	if err != nil {
		return err
	}
	acks, err := n.store.ListAcks(ctx, sc)
	if err != nil {
		return err
	}
	envs, err := n.store.ListEnvironments(ctx, sc)
	if err != nil {
		return err
	}
	prod := ""
	if len(envs) > 0 {
		prod = envs[len(envs)-1].ID // ordered by position
	}
	for _, u := range versions.LoadUpdates(ctx, n.store, sc, o, acks, now) {
		if u.Acked || u.Due.IsZero() {
			continue
		}
		left := u.DaysLeft(now)
		state := ""
		switch {
		case left < 0 && left >= -deadlineRecentDays:
			state = "passed"
		case left >= 0 && left <= deadlineWarnDays:
			state = "soon"
		default:
			continue
		}
		it := Item{
			Type: EventDeadline, Service: u.Service.Name, App: u.App, Owner: u.Service.Owner, Environment: u.Environment.Name,
			Production: u.Environment.ID == prod, From: u.Running, To: u.Target, At: now,
			Note: state + "|" + u.DueFor + "|" + u.Due.Format(time.DateOnly), URL: "/updates?due=over",
		}
		it.Text = Describe(it)
		dedup := "deadline|" + state + "|" + u.Service.ID + "|" + u.App + "|" + u.Environment.ID + "|" + u.Due.Format(time.DateOnly)
		if err := n.enqueue(ctx, sc, rules, it, dedup); err != nil {
			return err
		}
	}
	return nil
}

// riskAlerts asks for approval of the accepted risks waiting for it, once each, and says when one with a reason
// ends within deadlineWarnDays, once.
func (n *Notifier) riskAlerts(ctx context.Context, sc store.Scope, rules []store.Rule, now time.Time) error {
	pending, err := n.store.ListPendingAcks(ctx, sc)
	if err != nil {
		return err
	}
	approved, err := n.store.ListAcks(ctx, sc)
	if err != nil {
		return err
	}
	var svcs map[string]store.Service
	var envs []store.Environment
	for _, a := range append(pending, approved...) {
		state := "approve"
		if !a.Pending {
			left := a.UntilAt.Sub(now)
			if a.Reason == "" || a.UntilAt.IsZero() || left <= 0 || left > deadlineWarnDays*24*time.Hour {
				continue
			}
			state = "ends"
		} else if !a.UntilAt.IsZero() && !a.UntilAt.After(now) {
			continue
		}
		if svcs == nil {
			list, err := n.store.ListServices(ctx, sc)
			if err != nil {
				return err
			}
			svcs = map[string]store.Service{}
			for _, s := range list {
				svcs[s.ID] = s
			}
			if envs, err = n.store.ListEnvironments(ctx, sc); err != nil {
				return err
			}
		}
		svc := svcs[a.ServiceID]
		it := Item{
			Type: EventDeadline, Service: svc.Name, Owner: svc.Owner, At: now, URL: "/security#risks",
			Note: state + "|" + a.Reason + "|" + a.UntilAt.Format(time.DateOnly) + "|" + a.CreatedBy,
		}
		for i, e := range envs {
			if e.ID == a.EnvironmentID {
				it.Environment, it.Production = e.Name, i == len(envs)-1
			}
		}
		it.Text = Describe(it)
		if err := n.enqueue(ctx, sc, rules, it, "risk|"+state+"|"+a.ID); err != nil {
			return err
		}
	}
	return nil
}

// describeDeadline is a deadline item in words: "payments-api @ prod: the deadline for an exploited vulnerability
// passed on 22 Oct; 15.6 runs, 15.8 fixes it".
func describeDeadline(where string, it Item) string {
	state, rest, _ := strings.Cut(it.Note, "|")
	why, rest, _ := strings.Cut(rest, "|")
	date, by, _ := strings.Cut(rest, "|")
	when := date
	if d, err := time.Parse(time.DateOnly, date); err == nil {
		when = d.Format("2 Jan")
	}
	switch state {
	case "approve":
		return fmt.Sprintf("%s: %s accepts the risk until %s and waits for an admin's approval: “%s”", where, orDash(by), when, why)
	case "ends":
		return fmt.Sprintf("%s: the accepted risk ends on %s (“%s”); upgrade, or accept it again", where, when, why)
	}
	upgrade := ""
	if it.To != "" {
		upgrade = fmt.Sprintf("; %s runs, %s fixes it", orDash(it.From), it.To)
	}
	if state == "passed" {
		return fmt.Sprintf("%s: the remediation deadline for the %s passed on %s%s", where, why, when, upgrade)
	}
	return fmt.Sprintf("%s: the remediation deadline for the %s is %s%s", where, why, when, upgrade)
}
