// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package ui

import (
	"encoding/csv"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/pipozzz/goliash/internal/auth"
	"github.com/pipozzz/goliash/internal/store"
	"github.com/pipozzz/goliash/internal/versions"
)

// VMReportView is the vulnerability management report of one month: evidence for NIS2, ISO 27001 or SOC 2 audits.
type VMReportView struct {
	Base
	versions.VMReport
	Workspace, Month, Generated      string
	Prev, PrevLabel, Next, NextLabel string
	Now                              time.Time
}

// reportMonth is the month asked for (?month=2026-09), else the last full month.
func reportMonth(r *http.Request, now time.Time) (time.Time, bool) {
	month := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC).AddDate(0, -1, 0)
	if q := r.URL.Query().Get("month"); q != "" {
		m, err := time.Parse("2006-01", q)
		if err != nil {
			return month, false
		}
		month = m
	}
	return month, true
}

func (s *Server) vmReport(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	now := time.Now().UTC()
	month, ok := reportMonth(r, now)
	if !ok {
		return back(w, r, "/security/report", "error", "Pick a month like 2026-09.")
	}
	rep, err := versions.LoadVMReport(r.Context(), s.store, p.Scope, month, now)
	if err != nil {
		return err
	}
	v := VMReportView{
		Base: s.base(r.Context(), p, "security", "Vulnerability management "+month.Format("2006-01")), VMReport: rep,
		Workspace: p.Workspace.Name, Month: month.Format("January 2006"), Generated: now.Format("2 January 2006 15:04 UTC"),
		Prev: month.AddDate(0, -1, 0).Format("2006-01"), PrevLabel: month.AddDate(0, -1, 0).Format("January"), Now: now,
	}
	if next := month.AddDate(0, 1, 0); !next.After(now) {
		v.Next, v.NextLabel = next.Format("2006-01"), next.Format("January")
	}
	return render(w, r, VMReportPage(v))
}

func (s *Server) vmReportCSV(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	now := time.Now().UTC()
	month, ok := reportMonth(r, now)
	if !ok {
		http.Error(w, "Pick a month like 2026-09.", http.StatusBadRequest)
		return nil
	}
	rep, err := versions.LoadVMReport(r.Context(), s.store, p.Scope, month, now)
	if err != nil {
		return err
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="goliash-vulnerability-management-`+month.Format("2006-01")+`.csv"`)
	cw := csv.NewWriter(w)
	_ = cw.Write([]string{"service", "owner", "from", "to", "behind_since", "deployed", "days_to_patch", "security_fix", "within_deadline"})
	for _, u := range rep.Upgrades {
		in := ""
		if u.InTime != nil {
			in = strconv.FormatBool(*u.InTime)
		}
		_ = cw.Write([]string{
			u.Service, u.Owner, u.From, u.To, u.BehindSince.UTC().Format(time.DateOnly), u.At.UTC().Format(time.DateOnly),
			strconv.Itoa(u.Days), u.SecurityFix, in,
		})
	}
	cw.Flush()
	return cw.Error()
}

// slaText is the share of security fixes deployed in time, for the report's stats.
func slaText(r versions.VMReport) string {
	if p := r.SLAPercent(); p >= 0 {
		return itoa(p) + " %"
	}
	return "—"
}

// inTimeText says whether an upgrade met its security-fix deadline.
func inTimeText(u versions.VMUpgrade) string {
	switch {
	case u.InTime == nil:
		return ""
	case *u.InTime:
		return "in time"
	default:
		return "late"
	}
}

// policyDays says a deadline in days, or that none is set.
func policyDays(n int) string {
	if n == 0 {
		return "not set"
	}
	return plural(n, "day", "days")
}

// ackUntilText says when an acknowledgement ends.
func ackUntilText(a store.Ack) string {
	switch {
	case a.UntilVersion != "" && !a.UntilAt.IsZero():
		return a.UntilVersion + " or " + a.UntilAt.Format("2006-01-02")
	case a.UntilVersion != "":
		return "version " + a.UntilVersion
	case a.UntilAt.IsZero():
		return "until withdrawn"
	default:
		return a.UntilAt.Format("2006-01-02 15:04 UTC")
	}
}

// ackKindText says what an acknowledgement quiets.
func ackKindText(kind string) string {
	if kind == "drift" {
		return "drift from Git"
	}
	return "new releases"
}

// vmServices lists the services a vulnerability runs in, each once.
func vmServices(f versions.VulnFinding) string {
	var out []string
	seen := map[string]bool{}
	for _, a := range f.Affected {
		if !seen[a.Service] {
			seen[a.Service] = true
			out = append(out, a.Service)
		}
	}
	return strings.Join(out, ", ")
}
