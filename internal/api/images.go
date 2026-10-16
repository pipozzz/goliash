// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"errors"
	"net/http"
	"time"

	"github.com/pipozzz/goliash/internal/auth"
	"github.com/pipozzz/goliash/internal/store"
	"github.com/pipozzz/goliash/internal/versions"
)

// imageHistoryDays is how far back a service's image history goes.
const imageHistoryDays = 90

type apiImage struct {
	Environment    string       `json:"environment"`
	Image          string       `json:"image"`
	Version        string       `json:"version,omitempty"`
	Digest         string       `json:"digest"`
	SizeBytes      int64        `json:"size_bytes,omitempty"`
	OverBudget     bool         `json:"over_budget,omitempty"`
	Layers         int          `json:"layers,omitempty"`
	BaseImage      string       `json:"base_image,omitempty"`
	BaseStatus     string       `json:"base_status,omitempty"`
	BaseBehindDays int          `json:"base_behind_days,omitempty"`
	Evidence       *apiEvidence `json:"evidence,omitempty"`
}

type apiEvidence struct {
	Signed     bool `json:"signed"`
	SBOM       bool `json:"sbom"`
	Provenance bool `json:"provenance"`
}

type apiImagePeriod struct {
	Environment string     `json:"environment"`
	Version     string     `json:"version,omitempty"`
	Digest      string     `json:"digest"`
	SizeBytes   int64      `json:"size_bytes,omitempty"`
	Since       time.Time  `json:"since"`
	Until       *time.Time `json:"until,omitempty"`
}

type apiLayer struct {
	SizeBytes int64  `json:"size_bytes"`
	Command   string `json:"command,omitempty"`
}

type apiSizeChange struct {
	Environment  string     `json:"environment"`
	From         string     `json:"from"`
	To           string     `json:"to"`
	BeforeBytes  int64      `json:"before_bytes"`
	AfterBytes   int64      `json:"after_bytes"`
	Added        []apiLayer `json:"added"`
	Removed      []apiLayer `json:"removed"`
	AddedCount   int        `json:"added_count"`
	RemovedCount int        `json:"removed_count"`
}

func apiLayers(ls []store.ImageLayer) []apiLayer {
	out := make([]apiLayer, 0, len(ls))
	for _, l := range ls {
		out = append(out, apiLayer{SizeBytes: l.Size, Command: l.Command})
	}
	return out
}

// serviceImages is what a service's images are made of: per environment the running image's size against the
// budget, its base and supply-chain evidence; every image that ran in the last 90 days with its size; and what the
// last deploy changed in the layers. It answers "why did the image grow?".
func (h *PublicHandler) serviceImages(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	ctx := r.Context()
	svc, err := h.store.GetServiceByName(ctx, p.Scope, r.PathValue("name"))
	if errors.Is(err, store.ErrNotFound) {
		writeProblem(w, http.StatusNotFound, "Not found", "no such service")
		return
	}
	if err != nil {
		h.fail(w, r, err)
		return
	}
	envs, err := h.store.ListEnvironments(ctx, p.Scope)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	active, err := h.store.ListActiveInstances(ctx, p.Scope)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	evidence, err := h.store.ListImageEvidence(ctx, p.Scope)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	now := time.Now()
	periods, err := h.store.ServiceImagePeriods(ctx, p.Scope, svc.ID, now.AddDate(0, 0, -imageHistoryDays))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	envName := map[string]string{}
	for _, e := range envs {
		envName[e.ID] = e.Name
	}
	ev := func(image, digest string) (store.ImageEvidence, bool) {
		e, ok := evidence[versions.ParseImage(image).Repo()+"@"+digest]
		return e, ok
	}
	budget := versions.SizeBudget(svc)

	images := []apiImage{}
	seen := map[string]bool{}
	for _, e := range envs {
		for _, in := range active {
			if in.ServiceID != svc.ID || in.EnvironmentID != e.ID || !in.IsMain || seen[e.ID+in.Image+in.Digest] {
				continue
			}
			seen[e.ID+in.Image+in.Digest] = true
			img := apiImage{Environment: e.Name, Image: in.Image, Version: in.Tag, Digest: in.Digest}
			if x, ok := ev(in.Image, in.Digest); ok && in.Digest != "" {
				img.SizeBytes, img.Layers = x.Size, len(x.Layers)
				img.OverBudget = budget > 0 && x.Size > budget
				img.BaseImage, img.BaseStatus = x.BaseImage, x.BaseStatus
				if x.BaseStatus == "outdated" && !x.BaseSince.IsZero() {
					img.BaseBehindDays = int(now.Sub(x.BaseSince).Hours() / 24)
				}
				if !x.CheckedAt.IsZero() && x.Error == "" {
					img.Evidence = &apiEvidence{Signed: x.Signed, SBOM: x.SBOM, Provenance: x.Provenance}
				}
			}
			images = append(images, img)
		}
	}

	history := []apiImagePeriod{}
	for _, pd := range periods {
		x, _ := ev(pd.Image, pd.Digest)
		hp := apiImagePeriod{Environment: envName[pd.EnvironmentID], Version: pd.Tag, Digest: pd.Digest, SizeBytes: x.Size, Since: pd.Since.UTC()}
		if !pd.Until.IsZero() {
			u := pd.Until.UTC()
			hp.Until = &u
		}
		history = append(history, hp)
	}

	var change *apiSizeChange
	if c := versions.LastSizeChange(periods, evidence, envs); c != nil {
		change = &apiSizeChange{
			Environment: c.Env, From: c.From, To: c.To, BeforeBytes: c.Before, AfterBytes: c.After,
			Added: apiLayers(c.Added), Removed: apiLayers(c.Removed), AddedCount: c.AddedN, RemovedCount: c.RemovedN,
		}
	}

	out := struct {
		Service     string           `json:"service"`
		BudgetBytes int64            `json:"budget_bytes,omitempty"`
		Images      []apiImage       `json:"images"`
		History     []apiImagePeriod `json:"history"`
		LastChange  *apiSizeChange   `json:"last_change,omitempty"`
	}{svc.Name, budget, images, history, change}
	writeJSON(w, http.StatusOK, out)
}
