// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package ui

import (
	"encoding/csv"
	"net/http"
	"net/url"
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
	Licenses versions.LicenseReport
	NoDigest int // targets reporting no digest at all
	At       time.Time
	Trend    []TrendLine // production over the last 90 days; empty until there are two days
	Since    string      // the first day of the trend
}

// TrendLine is one number of the posture over time.
type TrendLine struct {
	Label    string
	Values   []int
	Max      int
	Now, Was int
	Days     bool // the number is days, not a count
}

// trendKept is how far back the Security page draws the posture.
const trendKept = 90

// postureTrend draws the posture's history: fewer behind, for less long, is better.
func postureTrend(hist []store.PostureSample) ([]TrendLine, string) {
	if len(hist) < 2 {
		return nil, ""
	}
	pick := []struct {
		label string
		days  bool
		get   func(store.PostureSample) int
	}{
		{"Median exposure", true, func(p store.PostureSample) int { return p.Median }},
		{"Behind a newer release", false, func(p store.PostureSample) int { return p.Exposed }},
		{"Behind for 90+ days", false, func(p store.PostureSample) int { return p.Behind90 }},
		{"Past end of life", false, func(p store.PostureSample) int { return p.EOLPassed }},
	}
	out := make([]TrendLine, len(pick))
	for i, k := range pick {
		t := TrendLine{Label: k.label, Days: k.days}
		for _, p := range hist {
			v := k.get(p)
			t.Values = append(t.Values, v)
			t.Max = max(t.Max, v)
		}
		t.Now, t.Was = t.Values[len(t.Values)-1], t.Values[0]
		out[i] = t
	}
	return out, hist[0].Day
}

// trendText says where a number went: "43 days, was 51".
func trendText(t TrendLine) string {
	now, was := itoa(t.Now), itoa(t.Was)
	if t.Days {
		now, was = days(t.Now), days(t.Was)
	}
	if t.Now == t.Was {
		return now + ", unchanged"
	}
	return now + ", was " + was
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
	if v.Licenses, err = versions.LoadLicenseReport(r.Context(), s.store, p.Scope, v.Prod.ID); err != nil {
		return err
	}
	if v.Vulns, err = versions.LoadVulnReport(r.Context(), s.store, p.Scope, v.Prod.ID); err != nil {
		return err
	}
	if hist, err := s.store.PostureHistory(r.Context(), p.Scope, time.Now().AddDate(0, 0, -trendKept).UTC().Format(time.DateOnly)); err == nil {
		v.Trend, v.Since = postureTrend(hist)
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
	Signers                  []SignerCount  // who signed the signed images, most images first
	NoSigner                 int            // signed images whose signatures name nobody (a key)
}

// SignerCount is a signer and how many production images it signed.
type SignerCount struct {
	Identity, Issuer string
	Images           int
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
			if len(ev.Signers) == 0 && ev.SignersRead {
				out.NoSigner++
			}
			for _, sg := range ev.Signers {
				counted := false
				for i := range out.Signers {
					if out.Signers[i].Identity == sg.Identity {
						out.Signers[i].Images++
						counted = true
					}
				}
				if !counted {
					out.Signers = append(out.Signers, SignerCount{Identity: sg.Identity, Issuer: sg.Issuer, Images: 1})
				}
			}
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
	sort.Slice(out.Signers, func(i, j int) bool {
		if out.Signers[i].Images != out.Signers[j].Images {
			return out.Signers[i].Images > out.Signers[j].Images
		}
		return out.Signers[i].Identity < out.Signers[j].Identity
	})
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
	OnlyKEV  bool // only vulnerabilities exploited in the wild
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
		OnlyKEV: r.URL.Query().Get("kev") == "1",
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
		if (q != "" && !versions.FindingMatches(f, q)) || (v.OnlyKEV && f.Exploited == nil) {
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

// vulnURL links an advisory to osv.dev.
func vulnURL(id string) string { return "https://osv.dev/vulnerability/" + id }

// PackageUse is one package of a finding and where it runs.
type PackageUse struct {
	Package string
	Fix     string
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
			out = append(out, PackageUse{Package: a.Package, Fix: a.Fix})
		}
		w := a.Service + " (" + a.Env + ")"
		if !slices.Contains(out[i].Where, w) {
			out[i].Where = append(out[i].Where, w)
		}
	}
	return out
}

// vulnsHref links the vulnerability list with its filters.
func vulnsHref(v VulnsView, key, value string) string {
	q := url.Values{}
	if v.Query != "" {
		q.Set("q", v.Query)
	}
	if v.All {
		q.Set("env", "all")
	}
	if v.OnlyKEV {
		q.Set("kev", "1")
	}
	if value == "" {
		q.Del(key)
	} else {
		q.Set(key, value)
	}
	if len(q) == 0 {
		return "/security/vulns"
	}
	return "/security/vulns?" + q.Encode()
}

// LicensesView lists the licenses of the packages in running images, those that need a look first.
type LicensesView struct {
	Base
	versions.LicenseReport
	Query   string
	All     bool   // every environment, not only the last one
	EnvName string // the environment shown when not All
	Class   string // "" packages that need a look, "all", or one class
	WithOS  bool   // operating system packages too
	Shown   []versions.LicensedPackage
	Hidden  int // packages not shown (the list is long)
	OSShown int // operating system packages left out, when WithOS is off
}

// licenseLabels name the license classes as people read them.
var licenseLabels = map[versions.LicenseClass]string{
	versions.NetworkCopyleft: "network copyleft", versions.SourceAvailable: "source-available",
	versions.StrongCopyleft: "strong copyleft", versions.WeakCopyleft: "weak copyleft",
	versions.UnknownLicense: "unknown", versions.Permissive: "permissive",
}

// licenseHints say what a license class asks.
var licenseHints = map[versions.LicenseClass]string{
	versions.NetworkCopyleft: "AGPL, SSPL: offering it as a service can oblige you to publish your source",
	versions.SourceAvailable: "BUSL, Elastic, PolyForm: limits on production, commercial or competing use",
	versions.StrongCopyleft:  "GPL: shipping it with your code can oblige you to publish that code",
	versions.WeakCopyleft:    "LGPL, MPL, EPL: changes to the package itself must be shared",
	versions.UnknownLicense:  "the SBOM names no license, or one Goliash does not know",
	versions.Permissive:      "MIT, Apache, BSD: attribution only",
}

func (s *Server) licenses(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	ctx := r.Context()
	envs, err := s.store.ListEnvironments(ctx, p.Scope)
	if err != nil {
		return err
	}
	q := r.URL.Query()
	v := LicensesView{
		Base: s.base(ctx, p, "security", "Licenses"), Query: strings.TrimSpace(q.Get("q")),
		All: q.Get("env") == "all", Class: q.Get("class"), WithOS: q.Get("os") == "1",
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
	if v.LicenseReport, err = versions.LoadLicenseReport(ctx, s.store, p.Scope, envID); err != nil {
		return err
	}
	for _, pkg := range v.Packages {
		switch {
		case v.Class == "" && !pkg.Class.Attention(),
			v.Class != "" && v.Class != "all" && string(pkg.Class) != v.Class,
			!versions.LicenseMatches(pkg, v.Query):
			continue
		case pkg.OS && !v.WithOS:
			v.OSShown++
			continue
		}
		if len(v.Shown) >= 500 {
			v.Hidden++
			continue
		}
		v.Shown = append(v.Shown, pkg)
	}
	return render(w, r, LicensesPage(v))
}

// licensesHref links the licenses page with one parameter changed.
func licensesHref(v LicensesView, key, value string) string {
	q := url.Values{}
	for k, val := range map[string]string{"q": v.Query, "class": v.Class} {
		if val != "" {
			q.Set(k, val)
		}
	}
	if v.All {
		q.Set("env", "all")
	}
	if v.WithOS {
		q.Set("os", "1")
	}
	if value == "" {
		q.Del(key)
	} else {
		q.Set(key, value)
	}
	if len(q) == 0 {
		return "/security/licenses"
	}
	return "/security/licenses?" + q.Encode()
}

// licenseNeedsLook counts the application packages whose license needs a look before shipping.
func licenseNeedsLook(r versions.LicenseReport) int {
	n := 0
	for c, k := range r.Counts {
		if c.Attention() {
			n += k
		}
	}
	return n
}

// whereRuns lists the services and environments a package runs in, each once.
func whereRuns(p versions.LicensedPackage) string {
	var out []string
	for _, u := range p.Where {
		out = append(out, u.Service+" ("+u.Environment+")")
	}
	return strings.Join(out, ", ")
}

// capitalize upper-cases a label's first letter.
func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// licenseSummary says how many application packages of each demanding class there are: "1 network copyleft,
// 2 unknown".
func licenseSummary(r versions.LicenseReport) string {
	var parts []string
	for _, c := range versions.LicenseClasses {
		if c.Attention() && r.Counts[c] > 0 {
			parts = append(parts, itoa(r.Counts[c])+" "+licenseLabels[c])
		}
	}
	return strings.Join(parts, ", ")
}

// shortSigner is a signer's identity as people read it: a GitHub or GitLab workflow as repository · file @ ref.
func shortSigner(identity string) string {
	s := identity
	for _, p := range []string{"https://github.com/", "https://gitlab.com/"} {
		s = strings.TrimPrefix(s, p)
	}
	s = strings.Replace(s, "/.github/workflows/", " · ", 1)
	s = strings.Replace(s, "//.gitlab-ci.yml", " · .gitlab-ci.yml", 1)
	s = strings.Replace(s, "@refs/heads/", " @ ", 1)
	s = strings.Replace(s, "@refs/tags/", " @ ", 1)
	return s
}

// issuerName names an OIDC issuer: GitHub Actions, GitLab, Google, or its URL.
func issuerName(issuer string) string {
	switch {
	case issuer == "https://token.actions.githubusercontent.com":
		return "GitHub Actions"
	case strings.HasPrefix(issuer, "https://gitlab."):
		return "GitLab"
	case issuer == "https://accounts.google.com":
		return "Google"
	case issuer == "https://github.com/login/oauth":
		return "GitHub"
	case strings.Contains(issuer, "login.microsoftonline.com"):
		return "Microsoft"
	}
	return issuer
}
