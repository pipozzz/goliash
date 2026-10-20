// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package notifier

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/pipozzz/goliash/internal/store"
	"github.com/pipozzz/goliash/internal/versions"
)

// EventGitOps announces a GitOps application (Argo CD, Flux) that has been out of sync, degraded or missing for 10 minutes:
// what Git declares does not run. Note is the trouble, From the application, To the commit last synced.
const EventGitOps = "gitops"

// CheckGitOps queues an alert for each application in trouble, once per workload, trouble and episode.
func (n *Notifier) CheckGitOps(ctx context.Context) error {
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
			wanted = wanted || len(r.EventTypes) == 0 || contains(r.EventTypes, EventGitOps)
		}
		if !wanted {
			continue
		}
		o, err := versions.LoadOverview(ctx, n.store, sc)
		if err != nil {
			return err
		}
		apps, err := versions.GitOpsApps(ctx, n.store, sc, o)
		if err != nil {
			return err
		}
		for _, a := range apps {
			trouble := a.Trouble(now)
			if trouble == "" {
				continue
			}
			it := Item{
				Type: EventGitOps, Service: a.Service, Owner: o.Services[a.ServiceID].Owner, Environment: a.Environment,
				Target: a.Target, Note: trouble, From: a.App, To: a.Revision, At: a.SyncSince, Tool: a.Tool,
				URL: versions.CommitWebURL(versions.RepoWebURL(a.Repo), a.Revision),
			}
			where := a.Path
			if a.TargetRevision != "" {
				where += " @ " + a.TargetRevision
			}
			it.Text = Describe(it) + fmt.Sprintf(" (%s)", where)
			if trouble == "out of sync" && n.pins != nil { // what did not go out
				if pinned := n.pins(ctx, a); pinned != "" {
					it.Text += fmt.Sprintf(": Git pins %s, %s runs", pinned, a.Tag)
				}
			}
			if trouble == "out of sync" { // and why it waits, when Goliash knows
				switch {
				case a.Blocked(now):
					it.Text += "; a sync window holds it (" + a.SyncBlocked + ")"
				case a.ManualSync:
					it.Text += "; Argo CD syncs it by hand"
				}
			}
			if cmp := versions.CompareWebURL(versions.RepoWebURL(a.Repo), a.PrevRevision, a.Revision); cmp != "" &&
				!a.RevisionAt.Before(a.SyncSince.Add(-24*time.Hour)) { // Git moved just before: link what changed
				it.URL = cmp
			}
			if trouble != "out of sync" { // unhealthy: say what changed just before, the likeliest cause
				if from, at, ok := versions.Cause(ctx, n.store, sc, a); ok {
					it.Text += fmt.Sprintf(", %s after %s replaced %s",
						since(at, a.SyncSince), a.Tag, from)
					if n.rollback != nil {
						if to := n.rollback(ctx, sc, a); to != "" { // one click from the alert to the pull request
							q := url.Values{"target_id": {a.TargetID}, "workload_id": {a.WorkloadID}, "to": {to}}
							it.Action, it.ActionPath = "Rollback PR → "+to, "/services/"+url.PathEscape(a.Service)+"/gitops/preview?"+q.Encode()
						}
					}
				}
			}
			dedup := fmt.Sprintf("%s|%s|%s|%s|%d", EventGitOps, trouble, a.TargetID, a.WorkloadID, a.SyncSince.Unix())
			if err := n.enqueue(ctx, sc, rules, it, dedup); err != nil {
				return err
			}
		}
	}
	return nil
}

// PullRequestFailing queues, once per pull request, the news that the checks of one Goliash opened fail.
func (n *Notifier) PullRequestFailing(sc store.Scope, it Item) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	rules, err := n.store.ActiveRules(ctx, sc)
	if err != nil {
		n.log.Error("queueing notifications failed", "err", err)
		return
	}
	it.Type, it.At, it.Note = EventPullRequest, n.now(), "failing"
	it.Text = Describe(it)
	if err := n.enqueue(ctx, sc, rules, it, EventPullRequest+"|failing|"+it.URL); err != nil {
		n.log.Error("queueing notifications failed", "err", err)
	}
}

// SetRollback lets an alert about a degraded application offer the rollback pull request Goliash would open.
func (n *Notifier) SetRollback(rollback func(ctx context.Context, sc store.Scope, a versions.GitOpsApp) string) {
	n.rollback = rollback
}

// actionURL is where an item's action button leads, or "".
func actionURL(base string, it Item) string {
	if base == "" || it.Action == "" || it.ActionPath == "" {
		return ""
	}
	return base + it.ActionPath
}

// SetGitPins lets an out-of-sync alert say which version Git pins, read through the GitOps repository's forge.
func (n *Notifier) SetGitPins(pins func(ctx context.Context, a versions.GitOpsApp) string) {
	n.pins = pins
}

// linkLabel names an item's link: release notes, or for an Argo CD alert the commit last synced.
func linkLabel(it Item) string {
	switch it.Type {
	case EventGitOps:
		if strings.Contains(it.URL, "/compare/") {
			return "Git changes"
		}
		return "Commit"
	case EventPullRequest:
		return "Pull request"
	}
	return "Release notes"
}

// EventPullRequest announces a pull (or merge) request Goliash opened to upgrade a service: by a person, or by the
// version policy (auto_pr). From and To are the versions, Note who opened it, URL the pull request.
const EventPullRequest = "pull_request"

// PullRequestOpened queues the announcement of a pull request Goliash opened.
func (n *Notifier) PullRequestOpened(sc store.Scope, it Item) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	rules, err := n.store.ActiveRules(ctx, sc)
	if err != nil {
		n.log.Error("queueing notifications failed", "err", err)
		return
	}
	it.Type, it.At = EventPullRequest, n.now()
	it.Text = Describe(it)
	if err := n.enqueue(ctx, sc, rules, it, EventPullRequest+"|"+it.URL); err != nil {
		n.log.Error("queueing notifications failed", "err", err)
	}
}

// since says how long after a change something followed it: "2 hours", "5 minutes"; "right" within a minute.
func since(change, then time.Time) string {
	d := then.Sub(change)
	switch {
	case d < time.Minute:
		return "right"
	case d < 2*time.Minute:
		return "a minute"
	case d < time.Hour:
		return fmt.Sprintf("%d minutes", int(d.Minutes()))
	case d < 2*time.Hour:
		return "an hour"
	case d < 48*time.Hour:
		return fmt.Sprintf("%d hours", int(d.Hours()))
	}
	return fmt.Sprintf("%d days", int(d.Hours()/24))
}

// pullWaitsAfter is how long a pull request Goliash opened may stay open before it is called out once: an
// upgrade proposed and then forgotten does nobody any good.
const pullWaitsAfter = 3 * 24 * time.Hour

// CheckWaitingPulls queues, once per pull request, a reminder of those Goliash opened that have waited for review
// longer than pullWaitsAfter (as the forge last said: open).
func (n *Notifier) CheckWaitingPulls(ctx context.Context) error {
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
			wanted = wanted || len(r.EventTypes) == 0 || contains(r.EventTypes, EventPullRequest)
		}
		if !wanted {
			continue
		}
		pulls, err := n.store.ListGitOpsPulls(ctx, sc)
		if err != nil {
			return err
		}
		var o versions.Overview
		for _, p := range pulls {
			if p.State != "open" || now.Sub(p.OpenedAt) < pullWaitsAfter {
				continue
			}
			if o.Services == nil {
				if o, err = versions.LoadOverview(ctx, n.store, sc); err != nil {
					return err
				}
			}
			target := ""
			for _, t := range o.Targets {
				if t.ID == p.TargetID {
					target = t.Name
				}
			}
			svc := o.Services[p.ServiceID]
			it := Item{
				Type: EventPullRequest, Service: svc.Name, Owner: svc.Owner, Environment: p.Environment, Target: target,
				From: p.From, To: p.To, URL: p.URL, At: now, Note: fmt.Sprintf("waiting %d", int(now.Sub(p.OpenedAt).Hours()/24)),
			}
			it.Text = Describe(it)
			if err := n.enqueue(ctx, sc, rules, it, EventPullRequest+"|waiting|"+p.URL); err != nil {
				return err
			}
		}
	}
	return nil
}
