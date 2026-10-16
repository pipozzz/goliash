// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"net/http"
	"time"

	"github.com/pipozzz/goliash/internal/auth"
	"github.com/pipozzz/goliash/internal/versions"
)

// rollouts lists the workloads whose replicas are not where they should be: none running, a rollout stuck with
// two versions side by side, or fewer replicas than asked for.
func (h *PublicHandler) rollouts(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	o, err := versions.LoadOverview(r.Context(), h.store, p.Scope)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	issues, err := versions.RolloutIssues(r.Context(), h.store, p.Scope, o, time.Now())
	if err != nil {
		h.fail(w, r, err)
		return
	}
	type apiVersion struct {
		Version string    `json:"version"`
		Running int       `json:"running"`
		Since   time.Time `json:"since"`
	}
	type apiRollout struct {
		Kind        string       `json:"kind"`
		Service     string       `json:"service"`
		Environment string       `json:"environment"`
		Target      string       `json:"target"`
		Workload    string       `json:"workload"`
		Running     int          `json:"running"`
		Desired     int          `json:"desired"`
		Since       time.Time    `json:"since"`
		Versions    []apiVersion `json:"versions"`
	}
	out := []apiRollout{}
	for _, is := range issues {
		ar := apiRollout{
			Kind: string(is.Kind), Service: is.Service, Environment: is.Environment, Target: is.Target, Workload: is.Workload,
			Running: is.Running, Desired: is.Desired, Since: is.Since.UTC(), Versions: []apiVersion{},
		}
		for _, v := range is.Versions {
			ar.Versions = append(ar.Versions, apiVersion{v.Version, v.Running, v.Since.UTC()})
		}
		out = append(out, ar)
	}
	writeJSON(w, http.StatusOK, out)
}
