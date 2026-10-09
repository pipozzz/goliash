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
	"github.com/pipozzz/goliash/internal/versions"
)

// SecurityView is the security posture: how long production runs behind available releases, end of life,
// supply-chain findings and blind spots, for whoever answers for the risk.
type SecurityView struct {
	Base
	versions.Posture
	All      bool // every environment, not only production
	Hygiene  map[string]int
	DigestOK bool // every target reports digests
	Bases    []BaseRow
	NoDigest int // targets reporting no digest at all
	At       time.Time
}

func (s *Server) loadPosture(r *http.Request, p auth.Principal) (versions.Posture, map[string]int, error) {
	ctx := r.Context()
	o, err := versions.LoadOverview(ctx, s.store, p.Scope)
	if err != nil {
		return versions.Posture{}, nil, err
	}
	pos, err := versions.LoadPosture(ctx, s.store, p.Scope, o, time.Now())
	if err != nil {
		return pos, nil, err
	}
	findings, err := versions.LoadHygiene(ctx, s.store, p.Scope)
	if err != nil {
		return pos, nil, err
	}
	hyg := map[string]int{}
	for _, f := range findings {
		hyg[f.Kind]++
	}
	return pos, hyg, nil
}

func (s *Server) security(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	pos, hyg, err := s.loadPosture(r, p)
	if err != nil {
		return err
	}
	v := SecurityView{Base: s.base(r.Context(), p, "security", "Security posture"), Posture: pos, All: r.URL.Query().Get("env") == "all", Hygiene: hyg, At: time.Now()}
	if v.Bases, err = s.baseRows(r, p); err != nil {
		return err
	}
	if hints, err := s.digestHints(r.Context(), p.Scope); err == nil {
		v.DigestOK, v.NoDigest = len(hints) == 0, len(hints)
	}
	if !v.All {
		prod := v.Exposures[:0:0]
		for _, ex := range v.Exposures {
			if ex.Environment.ID == v.Prod.ID {
				prod = append(prod, ex)
			}
		}
		v.Exposures = prod
	}
	return render(w, r, SecurityPage(v))
}

// securityCSV is every exposure as a spreadsheet, for audits and risk registers.
func (s *Server) securityCSV(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	pos, _, err := s.loadPosture(r, p)
	if err != nil {
		return err
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="goliash-exposure-`+time.Now().UTC().Format("20060102")+`.csv"`)
	cw := csv.NewWriter(w)
	_ = cw.Write([]string{"service", "application", "environment", "targets", "running", "fix", "behind_since", "days_exposed", "date_known", "end_of_life", "eol_passed", "accepted_risk", "release_notes"})
	for _, ex := range pos.Exposures {
		_ = cw.Write([]string{
			ex.Service.Name, ex.App, ex.Environment.Name, strings.Join(ex.On, " "), ex.Running, ex.Target,
			ex.BehindSince.UTC().Format(time.DateOnly), strconv.Itoa(ex.Days), strconv.FormatBool(ex.Known), ex.EOL, strconv.FormatBool(ex.EOLPassed),
			strconv.FormatBool(ex.Acked), ex.TargetURL,
		})
	}
	cw.Flush()
	return cw.Error()
}

// exposureClass colours an exposure by its age, or by end of life.
func exposureClass(ex versions.Exposure) string {
	switch {
	case ex.EOLPassed:
		return "eol"
	case ex.Days > 90:
		return "eol"
	case ex.Days > 30:
		return "env"
	default:
		return "upstream"
	}
}

// BaseRow is a service built on a base image whose support has ended or ends soon.
type BaseRow struct {
	Service string
	versions.BaseStatus
}

// baseRows lists the running services built on a base image past or near its end of life.
func (s *Server) baseRows(r *http.Request, p auth.Principal) ([]BaseRow, error) {
	ctx := r.Context()
	o, err := versions.LoadOverview(ctx, s.store, p.Scope)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	seen := map[string]versions.BaseStatus{}
	var out []BaseRow
	for _, row := range o.Matrix.Rows {
		base := versions.BaseImage(row.Service)
		if base == "" {
			continue
		}
		st, ok := seen[base]
		if !ok {
			st = s.checker.BaseStatus(ctx, base, now)
			seen[base] = st
		}
		if st.Passed || st.Soon {
			out = append(out, BaseRow{Service: row.Service.Name, BaseStatus: st})
		}
	}
	return out, nil
}
