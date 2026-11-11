// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package ui

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/pipozzz/goliash/internal/auth"
	"github.com/pipozzz/goliash/internal/store"
)

// notAffectedLabels are the reasons a vulnerability does not affect a service, after VEX's justifications.
var notAffectedLabels = map[string]string{
	"fixed":              "already fixed in the version that runs (a backport, or a build the advisory does not know)",
	"not_present":        "the vulnerable component is not in the image",
	"not_reachable":      "the vulnerable code is never run or reached",
	"mitigated_upstream": "the software prevents it another way",
	"other":              "other",
}

// createVulnException records, for a service, that a vulnerability does not affect it (admins: it takes the
// vulnerability out of alerts, deadlines and the report's open findings).
func (s *Server) createVulnException(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	ctx := r.Context()
	if err := r.ParseForm(); err != nil {
		return err
	}
	f := r.PostForm
	svc, err := s.store.GetServiceByName(ctx, p.Scope, f.Get("service"))
	if err != nil {
		return back(w, r, "/security/vulns", "error", "Unknown service.")
	}
	ret := "/services/" + url.PathEscape(svc.Name)
	cve, why, note := strings.TrimSpace(f.Get("cve")), f.Get("justification"), strings.TrimSpace(f.Get("note"))
	if _, ok := notAffectedLabels[why]; !ok || cve == "" {
		return back(w, r, ret, "error", "Pick why it does not affect "+svc.Name+".")
	}
	if note == "" || len(note) > 1000 {
		return back(w, r, ret, "error", "Say in a sentence how you know, for whoever reviews it.")
	}
	days, err := strconv.Atoi(f.Get("review"))
	if err != nil || days < 7 || days > 365 {
		days = 90
	}
	if _, err := s.store.AddVulnException(ctx, store.VulnException{
		Scope: p.Scope, CVE: cve, ServiceID: svc.ID, Justification: why, Note: note,
		ReviewAt: time.Now().AddDate(0, 0, days), CreatedBy: p.Name(),
	}); err != nil {
		return err
	}
	s.audit(ctx, p, "vuln.not_affected", "cve", cve, "service", svc.Name, "justification", why, "note", note)
	s.hub.Publish(p.Scope.WorkspaceID)
	return back(w, r, ret, "notice", cve+" does not affect "+svc.Name+": left out until it is reviewed.")
}

// closeVulnException withdraws an exception: the vulnerability counts for the service again.
func (s *Server) closeVulnException(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	ret := r.FormValue("return")
	if !strings.HasPrefix(ret, "/services/") || strings.Contains(ret, "//") {
		ret = "/security/vulns"
	}
	if err := s.store.CloseVulnException(r.Context(), p.Scope, r.PathValue("id")); errors.Is(err, store.ErrNotFound) {
		return back(w, r, ret, "error", "Already withdrawn.")
	} else if err != nil {
		return err
	}
	s.audit(r.Context(), p, "vuln.not_affected.withdraw", "exception", r.PathValue("id"))
	s.hub.Publish(p.Scope.WorkspaceID)
	return back(w, r, ret, "notice", "Withdrawn: the vulnerability counts again.")
}
