// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package notifier

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/pipozzz/goliash/internal/versions"
)

// EventRollout announces a rollout that does not finish: a workload running two versions side by side for half an
// hour, or none of the replicas it asks for. Note is the kind: stuck or down.
const EventRollout = "rollout"

// CheckRollouts queues an alert for each workload whose rollout is stuck or that runs none of its replicas, once
// per workload and state. Fewer replicas than asked for alone is not announced: a pod restarting is not news.
func (n *Notifier) CheckRollouts(ctx context.Context) error {
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
		wanted := false
		for _, r := range rules {
			wanted = wanted || len(r.EventTypes) == 0 || contains(r.EventTypes, EventRollout)
		}
		if !wanted {
			continue
		}
		o, err := versions.LoadOverview(ctx, n.store, sc)
		if err != nil {
			return err
		}
		issues, err := versions.RolloutIssues(ctx, n.store, sc, o, now)
		if err != nil {
			return err
		}
		for _, is := range issues {
			if is.Kind == versions.RolloutShort {
				continue
			}
			it := Item{
				Type: EventRollout, Service: is.Service, Owner: o.Services[is.ServiceID].Owner, Environment: is.Environment,
				Target: is.Target, Note: string(is.Kind), At: is.Since,
			}
			if is.Kind == versions.RolloutDown {
				it.From = strconv.Itoa(is.Desired)
			} else {
				it.From = rolloutVersions(is)
			}
			it.Text = Describe(it)
			dedup := fmt.Sprintf("%s|%s|%s|%s|%d", EventRollout, is.Kind, is.TargetID, is.Workload, is.Since.Unix())
			if err := n.enqueue(ctx, sc, rules, it, dedup); err != nil {
				return err
			}
		}
	}
	return nil
}

// rolloutVersions says which versions run on how many replicas: "1.6.0 on 1 of 3, 1.5.0 on 2".
func rolloutVersions(is versions.RolloutIssue) string {
	parts := make([]string, 0, len(is.Versions))
	for i, v := range is.Versions {
		s := v.Version + " on " + strconv.Itoa(v.Running)
		if i == 0 {
			s += " of " + strconv.Itoa(is.Desired)
		}
		parts = append(parts, s)
	}
	return strings.Join(parts, ", ")
}
