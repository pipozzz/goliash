// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package ui

import (
	"context"
	"encoding/csv"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/pipozzz/goliash/internal/auth"
	"github.com/pipozzz/goliash/internal/notifier"
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
	PRs      map[string][]PromotePR // service ID|environment ID: pull requests offered for an exposure
	Show     string                 // the exposures shown: "" all, "30", "eol" or "accepted" (a tile clicked)
	Trend    []TrendLine            // production over the last 90 days; empty until there are two days
	Watches  []WatchView            // security watches, with what each covers
	CanWatch bool                   // a member may start and end watches
	// Deadlines are the workspace's remediation deadlines; an admin sets them. Overdue and DueSoon count the
	// upgrades past them and due within a week.
	Brief           notifier.Brief          // the Since yesterday panel: the security brief
	AlertsOutside   bool                    // a rule sends vulnerabilities to Slack, e-mail or another channel outside Goliash
	Teams           []versions.TeamPatching // production by team, slowest first
	PatchMonths     []versions.PatchMonth   // production's time to patch, month by month
	Deadlines       store.Remediation
	CanSetDeadlines bool
	Risks           []RiskView // accepted risks: those waiting for approval first, then those in force
	Overdue         int
	DueSoon         int
	Since           string // the first day of the trend
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
	v := SecurityView{Base: withFlash(s.base(r.Context(), p, "security", "Security posture"), r), Posture: pos, All: r.URL.Query().Get("env") == "all", Hygiene: hyg, At: time.Now()}
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
	sort.Slice(v.Vulns.Blind, func(i, j int) bool {
		return v.Vulns.Blind[i].Service+v.Vulns.Blind[i].Image < v.Vulns.Blind[j].Service+v.Vulns.Blind[j].Image
	})
	v.CanWatch = p.Can(store.RoleMember)
	if v.Watches, err = s.watchViews(r.Context(), p.Scope); err != nil {
		return err
	}
	if v.Brief, err = notifier.LoadBrief(r.Context(), s.store, p.Scope, v.At); err != nil {
		return err
	}
	if v.AlertsOutside, err = notifier.SecurityAlertsOutside(r.Context(), s.store, p.Scope); err != nil {
		return err
	}
	if err := s.patching(r, p, &v); err != nil {
		return err
	}
	v.CanSetDeadlines = p.Can(store.RoleAdmin)
	if v.Deadlines, err = s.store.Remediation(r.Context(), p.Scope); err != nil {
		return err
	}
	if v.Risks, err = s.acceptedRisks(r.Context(), p.Scope, time.Now()); err != nil {
		return err
	}
	if v.Deadlines.Any() {
		v.Overdue, v.DueSoon, err = s.dueCounts(r.Context(), p.Scope)
		if err != nil {
			return err
		}
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
	switch show := r.URL.Query().Get("show"); show {
	case "30", "eol", "accepted":
		v.Show = show
		shown := v.Exposures[:0:0]
		for _, ex := range v.Exposures {
			if show == "30" && ex.Days > 30 || show == "eol" && ex.EOLPassed || show == "accepted" && ex.Acked {
				shown = append(shown, ex)
			}
		}
		v.Exposures = shown
	}
	if p.Can(store.RoleMember) || s.demoPulls() {
		if v.PRs, err = s.exposurePRs(r.Context(), p.Scope, v.Exposures); err != nil {
			return err
		}
	}
	return render(w, r, SecurityPage(v))
}

// exposurePRs are the pull requests the Security page offers to close exposures: an upgrade to the newest
// acceptable release where a GitOps application deploys the service.
func (s *Server) exposurePRs(ctx context.Context, sc store.Scope, exs []versions.Exposure) (map[string][]PromotePR, error) {
	if s.pulls == nil || len(exs) == 0 {
		return nil, nil
	}
	o, err := versions.LoadOverview(ctx, s.store, sc)
	if err != nil {
		return nil, err
	}
	apps, err := versions.GitOpsApps(ctx, s.store, sc, o)
	if err != nil || len(apps) == 0 {
		return nil, err
	}
	out := map[string][]PromotePR{}
	for _, ex := range exs {
		if prs := s.upgradePRs(ctx, sc, o, apps, ex.Service.ID, ex.Environment.Name, ex.Running, ex.Target); len(prs) > 0 {
			out[ex.Service.ID+"|"+ex.Environment.ID] = prs
		}
	}
	return out, nil
}

// securityHref links the Security page with its environment choice and the exposures to show.
func securityHref(v SecurityView, show string) string {
	q := url.Values{}
	if v.All {
		q.Set("env", "all")
	}
	if show != "" {
		q.Set("show", show)
	}
	if len(q) == 0 {
		return "/security"
	}
	return "/security?" + q.Encode()
}

// securityHrefEnv links the Security page in production only or every environment, keeping what it shows.
func securityHrefEnv(v SecurityView, all bool) string {
	v.All = all
	return securityHref(v, v.Show) + "#exposure"
}

// securityShowLabel says what a tile shows: "behind for 30+ days".
func securityShowLabel(show string) string {
	return map[string]string{"30": "behind for 30+ days", "eol": "past end of life", "accepted": "accepted risk"}[show]
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
	Via     string // the image of its chain of bases built on this one, when it is not the service's own base
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
	// Further down the chains of bases: a private base built on a release cycle past its end of life.
	active, err := s.store.ListActiveInstances(ctx, p.Scope)
	if err != nil {
		return nil, err
	}
	evidence, err := s.store.ListImageEvidence(ctx, p.Scope)
	if err != nil {
		return nil, err
	}
	listed := map[string]bool{}
	for _, in := range active {
		ev, ok := evidence[versions.ParseImage(in.Image).Repo()+"@"+in.Digest]
		if !ok || in.ServiceID == "" || len(ev.BaseChain) < 2 {
			continue
		}
		name := o.Services[in.ServiceID].Name
		for _, l := range ev.BaseChain[1:] {
			if listed[name+"|"+l.Base] {
				continue
			}
			listed[name+"|"+l.Base] = true
			st, ok := seen[l.Base]
			if !ok {
				st = s.checker.BaseStatus(ctx, l.Base, now)
				seen[l.Base] = st
			}
			if st.Passed || st.Soon {
				out = append(out, BaseRow{Service: name, Via: versions.ShortImage(l.Image), BaseStatus: st})
			}
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
	Exposed  bool // only those in images reachable from outside
	// ExploitedDays is the remediation deadline for an exploited vulnerability, which mitigations of those with no
	// fix are due by; 0 without one.
	ExploitedDays int
}

func (s *Server) vulns(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	ctx := r.Context()
	envs, err := s.store.ListEnvironments(ctx, p.Scope)
	if err != nil {
		return err
	}
	v := VulnsView{
		Base: withFlash(s.base(ctx, p, "security", "Vulnerabilities"), r), Query: strings.TrimSpace(r.URL.Query().Get("q")),
		All: r.URL.Query().Get("env") == "all", Enabled: s.checker != nil && s.checker.OSVEnabled(),
		OnlyKEV: r.URL.Query().Get("kev") == "1", Exposed: r.URL.Query().Get("exposed") == "1",
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
	if rem, err := s.store.Remediation(ctx, p.Scope); err == nil {
		v.ExploitedDays = rem.Exploited
	}
	q := strings.ToUpper(v.Query)
	v.LooksCVE = versions.CVEOf(q) != "" || strings.HasPrefix(q, "GHSA-")
	for _, f := range v.Findings {
		if (q != "" && !versions.FindingMatches(f, q)) || (v.OnlyKEV && f.Exploited == nil) || (v.Exposed && !f.IsExposed()) {
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
// vulnsUnfiltered is the page without its exploited and exposed filters.
func vulnsUnfiltered(v VulnsView) string {
	v.OnlyKEV, v.Exposed = false, false
	return vulnsHref(v, "", "")
}

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
	if v.Exposed {
		q.Set("exposed", "1")
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

// licensesCSV downloads the packages the licenses page shows, with its filters but without its length limit.
func (s *Server) licensesCSV(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	ctx := r.Context()
	q := r.URL.Query()
	envID, _, err := s.lastEnv(ctx, p, q.Get("env") == "all")
	if err != nil {
		return err
	}
	rep, err := versions.LoadLicenseReport(ctx, s.store, p.Scope, envID)
	if err != nil {
		return err
	}
	class, withOS, query := q.Get("class"), q.Get("os") == "1", strings.TrimSpace(q.Get("q"))
	var pkgs []versions.LicensedPackage
	for _, pkg := range rep.Packages {
		if (class == "" && !pkg.Class.Attention()) || (class != "" && class != "all" && string(pkg.Class) != class) ||
			(pkg.OS && !withOS) || !versions.LicenseMatches(pkg, query) {
			continue
		}
		pkgs = append(pkgs, pkg)
	}
	csvHeaders(w, "licenses")
	return versions.WriteLicensesCSV(w, pkgs)
}

// vulnsCSV downloads the vulnerabilities the vulnerabilities page shows, with its filters but without its limit.
func (s *Server) vulnsCSV(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	ctx := r.Context()
	q := r.URL.Query()
	envID, _, err := s.lastEnv(ctx, p, q.Get("env") == "all")
	if err != nil {
		return err
	}
	rep, err := versions.LoadVulnReport(ctx, s.store, p.Scope, envID)
	if err != nil {
		return err
	}
	query, onlyKEV, exposed := strings.ToUpper(strings.TrimSpace(q.Get("q"))), q.Get("kev") == "1", q.Get("exposed") == "1"
	var findings []versions.VulnFinding
	for _, f := range rep.Findings {
		if (query != "" && !versions.FindingMatches(f, query)) || (onlyKEV && f.Exploited == nil) || (exposed && !f.IsExposed()) {
			continue
		}
		findings = append(findings, f)
	}
	csvHeaders(w, "vulnerabilities")
	return versions.WriteVulnsCSV(w, findings, time.Now())
}

// lastEnv is the last environment (production) unless all is asked for: its ID ("" for all) and name.
func (s *Server) lastEnv(ctx context.Context, p auth.Principal, all bool) (string, string, error) {
	envs, err := s.store.ListEnvironments(ctx, p.Scope)
	if err != nil {
		return "", "", err
	}
	id, name := "", ""
	for _, e := range envs {
		if id == "" || e.Position >= positionOf(envs, id) {
			id, name = e.ID, e.Name
		}
	}
	if all {
		return "", "", nil
	}
	return id, name, nil
}

func csvHeaders(w http.ResponseWriter, what string) {
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="goliash-`+what+`-`+time.Now().UTC().Format("20060102")+`.csv"`)
}

// licensesCSVHref downloads what the licenses page shows.
func licensesCSVHref(v LicensesView) string {
	return strings.Replace(licensesHref(v, "", ""), "/security/licenses", "/security/licenses.csv", 1)
}

// vulnsCSVHref downloads what the vulnerabilities page shows.
func vulnsCSVHref(v VulnsView) string {
	return strings.Replace(vulnsHref(v, "", ""), "/security/vulns", "/security/vulns.csv", 1)
}

// fixLink is where a blind spot is fixed.
type fixLink struct{ URL, Label string }

// unknownFix links where the fix for a service whose newest release is unknown is made: the registry credentials,
// or the service's version policy (its upstream repository and tag filter).
func unknownFix(u versions.UnknownService) fixLink {
	switch u.Where {
	case "registries":
		return fixLink{"/settings?registry=" + url.QueryEscape(versions.RegistryHost(u.Repo)) + "#registries", "Registry credentials"}
	case "policy", "upstream":
		return fixLink{"/services/" + url.PathEscape(u.Service) + "#policy", "Version policy"}
	}
	return fixLink{}
}

// blindShown bounds the images without an SBOM the Security page lists.
const blindShown = 20

// sbomHow is which of the Add an SBOM snippets fits a service's source repository.
func sbomHow(source string) string {
	switch {
	case strings.Contains(source, "github.com"):
		return "github"
	case strings.Contains(source, "gitlab"):
		return "gitlab"
	}
	return "buildx"
}

func sbomHowLabel(how string) string {
	return map[string]string{"github": "GitHub Actions", "gitlab": "GitLab CI", "buildx": "docker buildx"}[how]
}

// The Add an SBOM snippets: BuildKit's SBOM (syft) and provenance attestations, attached to the image in the registry.
const (
	sbomGitHub = `# .github/workflows/release.yml: the build-push step
- uses: docker/build-push-action@v6
  with:
    push: true
    tags: ${{ steps.meta.outputs.tags }}
    sbom: true              # an SPDX SBOM of every package in the image, attached to it
    provenance: mode=max    # what it was built from, including its base images`
	sbomGitLab = `# .gitlab-ci.yml
build:
  image: docker:27
  services: [docker:27-dind]
  script:
    - docker buildx create --use
    - docker buildx build --push --sbom=true --provenance=mode=max -t "$CI_REGISTRY_IMAGE:$CI_COMMIT_REF_SLUG" .`
	sbomBuildx = `docker buildx build --push \
  --sbom=true --provenance=mode=max \
  -t registry.example.com/team/app:1.2.3 .`
	sbomExisting = `# syft lists the packages, cosign attaches them to the image as an SBOM attestation
syft registry.example.com/team/app:1.2.3 -o spdx-json > sbom.spdx.json
cosign attest --yes --type spdxjson --predicate sbom.spdx.json registry.example.com/team/app:1.2.3`
)

// patching fills the Security page's patching by team and time to patch.
func (s *Server) patching(r *http.Request, p auth.Principal, v *SecurityView) error {
	ctx := r.Context()
	o, err := versions.LoadOverview(ctx, s.store, p.Scope)
	if err != nil {
		return err
	}
	acks, err := s.store.ListAcks(ctx, p.Scope)
	if err != nil {
		return err
	}
	now := time.Now()
	teams, samples, err := versions.LoadTeamPatching(ctx, s.store, p.Scope, o, v.Posture, versions.LoadUpdates(ctx, s.store, p.Scope, o, acks, now), now)
	if err != nil {
		return err
	}
	named := false
	for _, t := range teams {
		named = named || t.Owner != ""
	}
	if named {
		v.Teams = teams
	}
	if len(samples) > 0 {
		v.PatchMonths = versions.PatchByMonth(samples, now)
	}
	return nil
}

// monthBar is the height of a month's bar, out of the largest: 0 to 100.
func monthBar(days, most int) int {
	if days <= 0 || most <= 0 {
		return 0
	}
	return max(4, days*100/most)
}

// mostDays is the largest of the months' 90th percentiles, the chart's top.
func mostDays(ms []versions.PatchMonth) int {
	most := 0
	for _, m := range ms {
		most = max(most, m.P90, m.Median)
	}
	return most
}

// teamMost is the largest monthly median of a team, its bars' top.
func teamMost(t versions.TeamPatching) int {
	most := 0
	for _, d := range t.Monthly {
		most = max(most, d)
	}
	return most
}

// teamName is a team as the page shows it.
func teamName(owner string) string {
	if owner == "" {
		return "no owner"
	}
	return owner
}

// sectionLink is one of the Security page's sections in its On this page links: what it is, and what stands out.
type sectionLink struct {
	ID, Label, Note string
	Warn            bool
}

// securityLinks are the Security page's sections, in its order, those it shows.
func securityLinks(v SecurityView) []sectionLink {
	out := []sectionLink{{ID: "since-yesterday", Label: "Since yesterday"}}
	if v.Brief.Any() {
		out[0].Warn = true
	}
	out = append(out, sectionLink{ID: "exposure", Label: "Exposure", Note: itoa(v.ProdExposed()) + " behind", Warn: v.ProdExposed() > 0})
	if len(v.PatchMonths) > 0 {
		out = append(out, sectionLink{ID: "time-to-patch", Label: "Time to patch"})
	}
	if len(v.Teams) > 0 {
		out = append(out, sectionLink{ID: "teams", Label: "Teams"})
	}
	if len(v.Trend) > 0 {
		out = append(out, sectionLink{ID: "trend", Label: "Trend"})
	}
	if len(v.Bases) > 0 {
		out = append(out, sectionLink{ID: "bases", Label: "Unsupported bases", Note: itoa(len(v.Bases)), Warn: true})
	}
	if len(v.Vulns.Blind) > 0 {
		out = append(out, sectionLink{ID: "sbom", Label: "Without an SBOM", Note: itoa(len(v.Vulns.Blind))})
	}
	if fs := noFix(v.Vulns); len(fs) > 0 {
		l := sectionLink{ID: "no-fix", Label: "No fix yet"}
		if n := unmitigatedCount(v.Vulns); n > 0 {
			l.Note, l.Warn = itoa(n)+" not mitigated", true
		}
		out = append(out, l)
	}
	vul := sectionLink{ID: "vulnerabilities", Label: "Vulnerabilities"}
	if v.Vulns.Exploited > 0 {
		vul.Note, vul.Warn = itoa(v.Vulns.Exploited)+" exploited", true
	}
	out = append(out, vul)
	if v.Licenses.WithSBOM > 0 {
		out = append(out, sectionLink{ID: "licenses", Label: "Licenses"})
	}
	if v.Evidence.Images > 0 {
		out = append(out, sectionLink{ID: "evidence", Label: "Evidence"})
	}
	out = append(out, sectionLink{ID: "blind-spots", Label: "Blind spots"})
	if len(v.Watches) > 0 || v.CanWatch {
		w := sectionLink{ID: "watches", Label: "Watches"}
		if len(v.Watches) > 0 {
			w.Note = itoa(len(v.Watches))
		}
		out = append(out, w)
	}
	if v.Deadlines.Any() || v.CanSetDeadlines {
		d := sectionLink{ID: "deadlines", Label: "Deadlines"}
		if v.Overdue > 0 {
			d.Note, d.Warn = itoa(v.Overdue)+" past", true
		}
		out = append(out, d)
	}
	if len(v.Risks) > 0 || v.Deadlines.Approval {
		k := sectionLink{ID: "risks", Label: "Accepted risks"}
		if n := v.PendingRisks(); n > 0 {
			k.Note, k.Warn = itoa(n)+" to approve", true
		}
		out = append(out, k)
	}
	return out
}

// dayWord is "day" or "days" for n.
func dayWord(n int) string {
	if n == 1 {
		return "day"
	}
	return "days"
}

// exposedWays are the ways in to the images a vulnerability is in, each once.
func exposedWays(f versions.VulnFinding) []string {
	var out []string
	for _, a := range f.Affected {
		for _, w := range a.Exposed {
			if !slices.Contains(out, w) {
				out = append(out, w)
			}
		}
	}
	return out
}

// MitigationView is a mitigation in force, where it applies.
type MitigationView struct {
	ID, Where, Kind, Measure, Review string
}

var mitigationKindLabels = map[string]string{"waf": "WAF or proxy rule", "config": "feature off", "isolation": "isolated", "other": "other"}

// mitigationsOf are the mitigations in force for a finding, each once.
func mitigationsOf(f versions.VulnFinding) []MitigationView {
	var out []MitigationView
	seen := map[string]bool{}
	for _, a := range f.Affected {
		if m := a.Mitigation; m != nil && !seen[m.ID] {
			seen[m.ID] = true
			out = append(out, MitigationView{
				ID: m.ID, Where: a.Service + " (" + a.Env + ")", Kind: mitigationKindLabels[m.Kind], Measure: m.Measure,
				Review: m.ReviewAt.Format("2 Jan 2006"),
			})
		}
	}
	return out
}

// placesText lists where a vulnerability runs: "payments-api (prod), web (staging)".
func placesText(as []versions.Affected) string {
	parts := make([]string, len(as))
	for i, a := range as {
		parts[i] = a.Service + " (" + a.Env + ")"
	}
	return strings.Join(parts, ", ")
}

func (s *Server) createMitigation(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	ret := r.FormValue("return") // the vulnerabilities page, or the service page it was recorded on
	if ret != "/security/vulns" && !strings.HasPrefix(ret, "/security/vulns?") && (!strings.HasPrefix(ret, "/services/") || strings.Contains(ret, "//")) {
		ret = "/security/vulns"
	}
	svcID, envID, ok := strings.Cut(r.FormValue("place"), "|")
	kind, measure, cve := r.FormValue("kind"), strings.TrimSpace(r.FormValue("measure")), strings.TrimSpace(r.FormValue("cve"))
	days, err := strconv.Atoi(r.FormValue("review"))
	switch {
	case !ok || cve == "" || measure == "":
		return back(w, r, ret, "error", "Say where, and what was done.")
	case !slices.Contains(store.MitigationKinds, kind):
		return back(w, r, ret, "error", "Pick what kind of mitigation it is.")
	case err != nil || days < 1 || days > 180:
		return back(w, r, ret, "error", "Pick when to review it.")
	}
	if len(measure) > 500 {
		measure = measure[:500]
	}
	m, err := s.store.CreateMitigation(r.Context(), store.Mitigation{
		Scope: p.Scope, CVE: cve, ServiceID: svcID, EnvironmentID: envID, Kind: kind, Measure: measure,
		ReviewAt: time.Now().AddDate(0, 0, days), CreatedBy: p.Name(),
	})
	if err != nil {
		return err
	}
	s.audit(r.Context(), p, "mitigation.create", "cve", cve, "kind", kind, "measure", measure, "id", m.ID)
	return back(w, r, ret, "notice", "Mitigation recorded. Goliash closes it when a fix is out, and says so: then upgrade.")
}

func (s *Server) closeMitigation(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	id := r.PathValue("id")
	if err := s.store.CloseMitigation(r.Context(), p.Scope, id, "withdrawn by "+p.Name()); errors.Is(err, store.ErrNotFound) {
		return back(w, r, "/security/vulns?kev=1", "error", "It is no longer in force.")
	} else if err != nil {
		return err
	}
	s.audit(r.Context(), p, "mitigation.withdraw", "id", id)
	return back(w, r, "/security/vulns?kev=1", "notice", "Mitigation withdrawn.")
}

// noFix are the findings exploited in the wild with no fix, those not mitigated everywhere first.
func noFix(r versions.VulnReport) []versions.VulnFinding {
	var out []versions.VulnFinding
	for _, f := range r.Findings {
		if f.NoFix() {
			out = append(out, f)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return len(out[i].Unmitigated()) > 0 && len(out[j].Unmitigated()) == 0 })
	return out
}

// unmitigatedCount counts the places running an exploited vulnerability with no fix and no mitigation.
func unmitigatedCount(r versions.VulnReport) int {
	n := 0
	for _, f := range r.Findings {
		n += len(f.Unmitigated())
	}
	return n
}
