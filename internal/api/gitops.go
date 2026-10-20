// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"net/http"
	"time"

	"github.com/pipozzz/goliash/internal/auth"
	"github.com/pipozzz/goliash/internal/versions"
)

// gitOps lists the GitOps applications (Argo CD) that deploy the workspace's services, those that need attention
// first: what Git declares, the commit last synced, and whether it runs.
func (h *PublicHandler) gitOps(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	o, err := versions.LoadOverview(r.Context(), h.store, p.Scope)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	apps, err := versions.GitOpsApps(r.Context(), h.store, p.Scope, o)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	type apiApp struct {
		Tool           string     `json:"tool"`
		App            string     `json:"app"`
		Service        string     `json:"service"`
		Environment    string     `json:"environment"`
		Target         string     `json:"target"`
		Workload       string     `json:"workload"`
		Repo           string     `json:"repo"`
		Path           string     `json:"path"`
		TargetRevision string     `json:"target_revision"`
		Revision       string     `json:"revision"`
		Sync           string     `json:"sync"`
		Health         string     `json:"health"`
		Since          time.Time  `json:"since"`
		Trouble        string     `json:"trouble"`
		Version        string     `json:"version,omitempty"`
		PrevRevision   string     `json:"prev_revision,omitempty"`
		CompareURL     string     `json:"compare_url,omitempty"`
		AfterChange    *change    `json:"after_change,omitempty"`
		SyncBlocked    string     `json:"sync_blocked,omitempty"`
		ManualSync     bool       `json:"manual_sync,omitempty"`
		BlockedUntil   *time.Time `json:"sync_blocked_until,omitempty"`
	}
	now := time.Now()
	var trouble, fine []apiApp
	for _, a := range apps {
		x := apiApp{
			Tool: a.Tool, App: a.App, Service: a.Service, Environment: a.Environment, Target: a.Target, Workload: a.Workload,
			Repo: a.Repo, Path: a.Path, TargetRevision: a.TargetRevision, Revision: a.Revision, Sync: a.Sync, Health: a.Health,
			Since: a.SyncSince.UTC(), Trouble: a.Trouble(now), Version: a.Tag, PrevRevision: a.PrevRevision,
			CompareURL: versions.CompareWebURL(versions.RepoWebURL(a.Repo), a.PrevRevision, a.Revision),
		}
		x.ManualSync = a.ManualSync
		if a.Blocked(now) {
			x.SyncBlocked = a.SyncBlocked
			if !a.SyncBlockedUntil.IsZero() {
				until := a.SyncBlockedUntil.UTC()
				x.BlockedUntil = &until
			}
		}
		if x.Trouble == "degraded" || x.Trouble == "missing" { // unhealthy: the version change just before, the likeliest cause
			if from, at, ok := versions.Cause(r.Context(), h.store, p.Scope, a); ok {
				x.AfterChange = &change{From: from, To: a.Tag, At: at.UTC()}
			}
		}
		if x.Trouble != "" {
			trouble = append(trouble, x)
		} else {
			fine = append(fine, x)
		}
	}
	writeJSON(w, http.StatusOK, append(append([]apiApp{}, trouble...), fine...))
}

// change is a version change on a cluster: from, to, and when.
type change struct {
	From string    `json:"from"`
	To   string    `json:"to"`
	At   time.Time `json:"at"`
}

// pullRequests lists the pull requests Goliash opened to change a version in a GitOps repository, newest first:
// what became of them, and whether the version they pin runs.
func (h *PublicHandler) pullRequests(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	ctx := r.Context()
	o, err := versions.LoadOverview(ctx, h.store, p.Scope)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	apps, err := versions.GitOpsApps(ctx, h.store, p.Scope, o)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	pulls, err := h.store.ListGitOpsPulls(ctx, p.Scope)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	running, targetName := map[string]string{}, map[string]string{}
	for _, a := range apps {
		running[a.TargetID+"|"+a.WorkloadID] = a.Tag
		targetName[a.TargetID] = a.Target
	}
	type apiPull struct {
		Service     string    `json:"service"`
		Environment string    `json:"environment"`
		Target      string    `json:"target"`
		From        string    `json:"from"`
		To          string    `json:"to"`
		URL         string    `json:"url"`
		State       string    `json:"state"`
		OpenedBy    string    `json:"opened_by"`
		OpenedAt    time.Time `json:"opened_at"`
		Running     bool      `json:"running"`
	}
	out := []apiPull{}
	for _, pr := range pulls {
		out = append(out, apiPull{
			Service: o.Services[pr.ServiceID].Name, Environment: pr.Environment, Target: targetName[pr.TargetID],
			From: pr.From, To: pr.To, URL: pr.URL, State: pr.State, OpenedBy: pr.OpenedBy, OpenedAt: pr.OpenedAt.UTC(),
			Running: running[pr.TargetID+"|"+pr.WorkloadID] == pr.To,
		})
	}
	writeJSON(w, http.StatusOK, out)
}
