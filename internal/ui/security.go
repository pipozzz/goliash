// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package ui

import (
	"encoding/csv"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/pipozzz/goliash/internal/auth"
	"github.com/pipozzz/goliash/internal/store"
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
	Evidence EvidenceSummary
	Vulns    versions.VulnReport
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
	if v.Evidence, err = s.evidenceSummary(r, p, v.Prod.ID); err != nil {
		return err
	}
	if v.Vulns, err = versions.LoadVulnReport(r.Context(), s.store, p.Scope, v.Prod.ID); err != nil {
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

// EvidenceSummary is how many images running in production have signatures and attestations.
type EvidenceSummary struct {
	Images                   int // distinct images (repository and digest) running in production
	Checked                  int // looked up on their registry
	Signed, SBOM, Provenance int
	Unchecked                int            // private registries or no digest: not looked up by the server
	Unsigned                 []EvidenceItem // checked images without a signature
}

// EvidenceItem is one production image and what was found for it.
type EvidenceItem struct {
	Image string
	Found []string
}

// evidenceSummary counts the signatures and attestations of the images running in the last environment.
func (s *Server) evidenceSummary(r *http.Request, p auth.Principal, prodID string) (EvidenceSummary, error) {
	var out EvidenceSummary
	ctx := r.Context()
	active, err := s.store.ListActiveInstances(ctx, p.Scope)
	if err != nil {
		return out, err
	}
	known, err := s.store.ListImageEvidence(ctx, p.Scope)
	if err != nil {
		return out, err
	}
	seen := map[string]bool{}
	for _, in := range active {
		if in.EnvironmentID != prodID {
			continue
		}
		repo := versions.ParseImage(in.Image).Repo()
		key := repo + "@" + in.Digest
		if seen[key] {
			continue
		}
		seen[key] = true
		out.Images++
		ev, ok := known[key]
		if in.Digest == "" || !ok || ev.Error != "" {
			out.Unchecked++
			continue
		}
		out.Checked++
		if ev.Signed {
			out.Signed++
		} else {
			out.Unsigned = append(out.Unsigned, EvidenceItem{Image: strings.TrimPrefix(in.Image, "docker.io/"), Found: ev.Found})
		}
		if ev.SBOM {
			out.SBOM++
		}
		if ev.Provenance {
			out.Provenance++
		}
	}
	sort.Slice(out.Unsigned, func(i, j int) bool { return out.Unsigned[i].Image < out.Unsigned[j].Image })
	return out, nil
}

// VulnsView is the answer to "is this vulnerability running?" and the list of known vulnerabilities.
type VulnsView struct {
	Base
	versions.VulnReport
	Query    string
	All      bool // every environment, not only the last one
	EnvName  string
	Shown    []versions.VulnFinding
	Hidden   int  // findings not shown (the list is long)
	LooksCVE bool // the query is a vulnerability ID
	Enabled  bool // vulnerability lookups are on
}

func (s *Server) vulns(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	ctx := r.Context()
	envs, err := s.store.ListEnvironments(ctx, p.Scope)
	if err != nil {
		return err
	}
	v := VulnsView{
		Base: s.base(ctx, p, "security", "Vulnerabilities"), Query: strings.TrimSpace(r.URL.Query().Get("q")),
		All: r.URL.Query().Get("env") == "all", Enabled: s.checker != nil && s.checker.OSVEnabled(),
	}
	envID := ""
	for _, e := range envs {
		if envID == "" || e.Position >= positionOf(envs, envID) {
			envID, v.EnvName = e.ID, e.Name
		}
	}
	if v.All {
		envID = ""
	}
	if v.VulnReport, err = versions.LoadVulnReport(ctx, s.store, p.Scope, envID); err != nil {
		return err
	}
	q := strings.ToUpper(v.Query)
	v.LooksCVE = versions.CVEOf(q) != "" || strings.HasPrefix(q, "GHSA-")
	for _, f := range v.Findings {
		if q != "" && !findingMatches(f, q) {
			continue
		}
		if len(v.Shown) >= 300 {
			v.Hidden++
			continue
		}
		v.Shown = append(v.Shown, f)
	}
	return render(w, r, VulnsPage(v))
}

func positionOf(envs []store.Environment, id string) int {
	for _, e := range envs {
		if e.ID == id {
			return e.Position
		}
	}
	return -1
}

// findingMatches reports whether a finding answers the query: a CVE or advisory ID, a package, a service.
func findingMatches(f versions.VulnFinding, q string) bool {
	if strings.Contains(strings.ToUpper(f.Key), q) {
		return true
	}
	for _, id := range append(append([]string{}, f.IDs...), f.Aliases...) {
		if strings.Contains(strings.ToUpper(id), q) {
			return true
		}
	}
	for _, a := range f.Affected {
		if strings.Contains(strings.ToUpper(a.Package), q) || strings.Contains(strings.ToUpper(a.Service), q) {
			return true
		}
	}
	return false
}

// vulnURL links an advisory to osv.dev.
func vulnURL(id string) string { return "https://osv.dev/vulnerability/" + id }

// PackageUse is one package of a finding and where it runs.
type PackageUse struct {
	Package string
	Where   []string // "service (env)"
}

// byPackage groups a finding's running images by package, each service once.
func byPackage(f versions.VulnFinding) []PackageUse {
	var out []PackageUse
	index := map[string]int{}
	for _, a := range f.Affected {
		i, ok := index[a.Package]
		if !ok {
			i = len(out)
			index[a.Package] = i
			out = append(out, PackageUse{Package: a.Package})
		}
		w := a.Service + " (" + a.Env + ")"
		if !slices.Contains(out[i].Where, w) {
			out[i].Where = append(out[i].Where, w)
		}
	}
	return out
}
