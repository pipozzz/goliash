// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package notifier

import (
	"context"
	"fmt"

	"github.com/pipozzz/goliash/internal/versions"
)

// EventGitOps announces an Argo CD application that has been out of sync, degraded or missing for 10 minutes:
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
				Target: a.Target, Note: trouble, From: a.App, To: a.Revision, At: a.SyncSince,
				URL: versions.CommitWebURL(versions.RepoWebURL(a.Repo), a.Revision),
			}
			where := a.Path
			if a.TargetRevision != "" {
				where += " @ " + a.TargetRevision
			}
			it.Text = Describe(it) + fmt.Sprintf(" (%s)", where)
			dedup := fmt.Sprintf("%s|%s|%s|%s|%d", EventGitOps, trouble, a.TargetID, a.WorkloadID, a.SyncSince.Unix())
			if err := n.enqueue(ctx, sc, rules, it, dedup); err != nil {
				return err
			}
		}
	}
	return nil
}

// linkLabel names an item's link: release notes, or for an Argo CD alert the commit last synced.
func linkLabel(it Item) string {
	if it.Type == EventGitOps {
		return "Commit"
	}
	return "Release notes"
}
