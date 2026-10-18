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
		Tool           string    `json:"tool"`
		App            string    `json:"app"`
		Service        string    `json:"service"`
		Environment    string    `json:"environment"`
		Target         string    `json:"target"`
		Workload       string    `json:"workload"`
		Repo           string    `json:"repo"`
		Path           string    `json:"path"`
		TargetRevision string    `json:"target_revision"`
		Revision       string    `json:"revision"`
		Sync           string    `json:"sync"`
		Health         string    `json:"health"`
		Since          time.Time `json:"since"`
		Trouble        string    `json:"trouble"`
	}
	now := time.Now()
	var trouble, fine []apiApp
	for _, a := range apps {
		x := apiApp{
			Tool: a.Tool, App: a.App, Service: a.Service, Environment: a.Environment, Target: a.Target, Workload: a.Workload,
			Repo: a.Repo, Path: a.Path, TargetRevision: a.TargetRevision, Revision: a.Revision, Sync: a.Sync, Health: a.Health,
			Since: a.SyncSince.UTC(), Trouble: a.Trouble(now),
		}
		if x.Trouble != "" {
			trouble = append(trouble, x)
		} else {
			fine = append(fine, x)
		}
	}
	writeJSON(w, http.StatusOK, append(append([]apiApp{}, trouble...), fine...))
}
