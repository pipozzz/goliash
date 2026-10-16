// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"net/http"
	"strings"

	"github.com/pipozzz/goliash/internal/auth"
	"github.com/pipozzz/goliash/internal/versions"
)

// licenses answers what licenses the packages of running images are under, most demanding first: for a license
// review, or to check whether an AGPL package runs anywhere.
func (h *PublicHandler) licenses(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	q := r.URL.Query()
	envID := ""
	if name := q.Get("environment"); name != "" {
		env, err := h.store.GetEnvironmentByName(r.Context(), p.Scope, name)
		if err != nil {
			writeProblem(w, http.StatusNotFound, "Unknown environment", name)
			return
		}
		envID = env.ID
	}
	class := q.Get("class")
	if class != "" && class != "attention" && !validClass(class) {
		writeProblem(w, http.StatusBadRequest, "Bad request", "class must be attention or one of network-copyleft, source-available, strong-copyleft, weak-copyleft, unknown, permissive")
		return
	}
	withOS := q.Get("os") != "false"
	rep, err := versions.LoadLicenseReport(r.Context(), h.store, p.Scope, envID)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	type apiUse struct {
		Service     string `json:"service"`
		Environment string `json:"environment"`
	}
	type apiPackage struct {
		Purl      string   `json:"purl"`
		Name      string   `json:"name"`
		Version   string   `json:"version,omitempty"`
		License   string   `json:"license,omitempty"`
		Class     string   `json:"class"`
		OS        bool     `json:"os"`
		RunningIn []apiUse `json:"running_in"`
	}
	counts := func(m map[versions.LicenseClass]int) map[string]int {
		out := map[string]int{}
		for _, c := range versions.LicenseClasses {
			out[string(c)] = m[c]
		}
		return out
	}
	out := struct {
		Images   int            `json:"images"`
		WithSBOM int            `json:"images_with_sbom"`
		Counts   map[string]int `json:"counts"`
		OSCounts map[string]int `json:"os_counts"`
		Complete bool           `json:"complete"`
		Packages []apiPackage   `json:"packages"`
	}{rep.Images, rep.WithSBOM, counts(rep.Counts), counts(rep.OSCounts), rep.LicensesUnread == 0, []apiPackage{}}
	search := strings.TrimSpace(q.Get("q"))
	var shown []versions.LicensedPackage
	for _, pkg := range rep.Packages {
		if (class == "attention" && !pkg.Class.Attention()) || (class != "" && class != "attention" && string(pkg.Class) != class) ||
			(pkg.OS && !withOS) || !versions.LicenseMatches(pkg, search) {
			continue
		}
		shown = append(shown, pkg)
	}
	if q.Get("format") == "csv" {
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		_ = versions.WriteLicensesCSV(w, shown)
		return
	}
	for _, pkg := range shown {
		ap := apiPackage{Purl: pkg.Purl, Name: pkg.Name, Version: pkg.Version, License: pkg.License, Class: string(pkg.Class), OS: pkg.OS, RunningIn: []apiUse{}}
		for _, u := range pkg.Where {
			ap.RunningIn = append(ap.RunningIn, apiUse{u.Service, u.Environment})
		}
		out.Packages = append(out.Packages, ap)
	}
	writeJSON(w, http.StatusOK, out)
}

func validClass(c string) bool {
	for _, x := range versions.LicenseClasses {
		if string(x) == c {
			return true
		}
	}
	return false
}
