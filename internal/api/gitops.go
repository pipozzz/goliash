// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"net/http"
	"strconv"
	"strings"
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
	pulls, err := versions.PullTimings(ctx, h.store, p.Scope, apps, time.Time{})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	targetName := map[string]string{}
	for _, a := range apps {
		targetName[a.TargetID] = a.Target
	}
	type apiPull struct {
		Service               string     `json:"service"`
		Environment           string     `json:"environment"`
		Target                string     `json:"target"`
		From                  string     `json:"from"`
		To                    string     `json:"to"`
		URL                   string     `json:"url"`
		State                 string     `json:"state"`
		Checks                string     `json:"checks,omitempty"`
		OpenedBy              string     `json:"opened_by"`
		OpenedAt              time.Time  `json:"opened_at"`
		MergedAt              *time.Time `json:"merged_at,omitempty"`
		Running               bool       `json:"running"`
		Waiting               string     `json:"waiting,omitempty"`
		ToRunningSeconds      int64      `json:"to_running_seconds,omitempty"`
		MergeToRunningSeconds int64      `json:"merge_to_running_seconds,omitempty"`
	}
	now := time.Now()
	out := []apiPull{}
	for _, pr := range pulls {
		x := apiPull{
			Service: o.Services[pr.ServiceID].Name, Environment: pr.Environment, Target: targetName[pr.TargetID],
			From: pr.From, To: pr.To, URL: pr.URL, State: pr.State, Checks: pr.Checks, OpenedBy: pr.OpenedBy,
			OpenedAt: pr.OpenedAt.UTC(), Running: pr.Deployed,
			ToRunningSeconds: int64(pr.ToRunning.Seconds()), MergeToRunningSeconds: int64(pr.MergeToRunning.Seconds()),
		}
		if !pr.MergedAt.IsZero() {
			at := pr.MergedAt.UTC()
			x.MergedAt = &at
		}
		if pr.Undeployed() {
			x.Waiting = versions.WhyWaiting(pr.App, now)
			if x.Waiting == "" {
				x.Waiting = "merged; the version does not run yet"
			}
		}
		out = append(out, x)
	}
	writeJSON(w, http.StatusOK, out)
}

// search looks for q through services (by what they run too), vulnerabilities, packages, pull requests, history
// and notifications, as the search page does.
func (h *PublicHandler) search(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" {
		writeProblem(w, http.StatusBadRequest, "Bad request", "q is required: the words to look for")
		return
	}
	limit := 10
	if n, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && n > 0 {
		limit = min(n, 50)
	}
	sections, err := versions.Search(r.Context(), h.store, p.Scope, q, limit, time.Now())
	if err != nil {
		h.fail(w, r, err)
		return
	}
	total := 0
	for i := range sections {
		total += sections[i].Total
		if sections[i].Hits == nil {
			sections[i].Hits = []versions.SearchHit{}
		}
	}
	writeJSON(w, http.StatusOK, struct {
		Query    string                   `json:"query"`
		Total    int                      `json:"total"`
		Sections []versions.SearchSection `json:"sections"`
	}{q, total, sections})
}
