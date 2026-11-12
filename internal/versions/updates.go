// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package versions

import (
	"context"
	"encoding/json"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/pipozzz/goliash/internal/store"
)

// Update is one thing to upgrade: a service (within one application, when it is
// compared per application) in one environment, from what runs to what it should.
type Update struct {
	Service     store.Service
	App         string
	Environment store.Environment
	Running     string
	Target      string // the version to move to; empty when only the end of life is known
	TargetURL   string // its release notes
	Jump        Jump
	Behind      string // the previous environment it is behind, if any
	// SecurityFix is what the releases up to Target say they fix: CVE or other identifiers, or "security".
	SecurityFix string
	// Breaking is what the releases up to Target say breaks, quoted from their notes: "2.0.0: Removed …".
	Breaking []string
	// Path is the versions the upgrade goes through, Target last, for a product that cannot skip versions
	// (Kubernetes one minor at a time); nil when it can go straight to Target. PathNote says why.
	Path     []string
	PathNote string
	// Risk is how much the upgrade may break (RiskLow, RiskMedium, RiskHigh), RiskReasons why; empty without Target.
	Risk        string
	RiskReasons []string
	// MaintenanceOnly is a line for what waits at the platform alone (a reboot, a managed service's pending
	// patches), with no newer version to move to: Remedy says what to do.
	MaintenanceOnly bool
	// Trouble is what the version to move to (or a stop on the way) did where it ran: "1.28.0 rolled back in prod
	// (payments) on 2026-11-09"; empty when nothing is known.
	Trouble   string
	EOL       string // the end-of-life date of the running release cycle, if it is near or past
	EOLPassed bool
	On        []string  // the targets running Running, when the environment runs other versions too
	Since     time.Time // when the oldest of its drifts opened
	Acked     bool      // an acknowledgement quiets it
	Urgency   int       // 0 most urgent; see urgencyLabels
	// Exploited lists the vulnerabilities in the running image that CISA knows to be exploited in the wild;
	// Critical counts its other critical ones. Both come from the image's SBOM, when Goliash could read it.
	Exploited []string
	Critical  int
	// Exposed is how the running version can be reached from outside its cluster or host, if it can: within an
	// urgency level, exposed updates come first.
	Exposed []string
	// Maintenance is the patches the platform has waiting for it (a managed service's pending maintenance).
	Maintenance []string
	// ExtendedSupport is what AWS charges for the managed service past the end of standard support (EOL), when it
	// runs on one AWS keeps running for a fee.
	ExtendedSupport *ExtendedSupport
	// Remedy is what fixes an exploited vulnerability when no newer release is known (Target is empty): a rebuild
	// on the current build of its base, a newer release of its distribution, or a newer package.
	Remedy string
	// ExploitedSince is when the first of Exploited became known to run exploited: the later of the image running
	// there and CISA listing it.
	ExploitedSince time.Time
	// SecuritySince is when Goliash found the first release up to Target that fixes vulnerabilities.
	SecuritySince time.Time
	// Due is the remediation deadline the workspace set for its most pressing reason (DueFor); zero without one.
	Due    time.Time
	DueFor string
	// Vulns is what upgrading does to known vulnerabilities, when the target version already runs somewhere and
	// both images have an SBOM: those it fixes and those it brings.
	Vulns VulnDelta
	// Servers are the targets whose operating system this is, for a Linux server's OS: the IDs of the servers the
	// line is about, since every server of an environment shares one line.
	Servers []string
	// HostFixes say, for a server's exploited vulnerabilities, which of its packages has each and what fixes it.
	HostFixes []HostFix
}

// HostFix is an exploited vulnerability in a package of a server: what to upgrade on which server.
type HostFix struct {
	CVE, Server, Package string // Package is the name and version installed
	Fix                  string // the version that fixes it, empty when unknown
	FixNewer             bool   // a newer version fixes it, which one not known
}

// Remedy says what fixes it, in a few words.
func (h HostFix) Remedy() string {
	switch {
	case h.Fix != "":
		return "upgrade to " + h.Fix
	case h.FixNewer:
		return "upgrade to a newer version"
	}
	return "no fix known yet"
}

// Urgency levels, most urgent first.
const (
	UrgencyExploited = iota
	UrgencySecurity  // a release up to the target fixes vulnerabilities, says the project
	UrgencyEOLPassed
	UrgencyEOLSoon
	UrgencyMajor
	UrgencyMinor
	UrgencyBehindEnv
	UrgencyPatch
)

// UrgencyLabels name the levels.
var UrgencyLabels = []string{"exploited vulnerability", "security fix", "end of life", "end of life soon", "major", "minor", "behind previous env", "patch"}

// Updates lists what to upgrade, most urgent first: open end-of-life, upstream and
// environment drift, one item per service, application and environment.
func Updates(o Overview, acks []store.Ack, now time.Time) []Update {
	type key struct{ svc, app, env string }
	byKey := map[key]*Update{}
	var order []key
	for _, ds := range o.Drifts {
		for _, d := range ds {
			if d.Kind != "eol" && d.Kind != "upstream" && d.Kind != "env" {
				continue
			}
			k := key{d.ServiceID, d.App, d.EnvironmentID}
			u := byKey[k]
			if u == nil {
				u = &Update{Service: o.Services[d.ServiceID], App: d.App, Environment: o.Envs[d.EnvironmentID], Since: d.Since, Urgency: UrgencyPatch}
				byKey[k] = u
				order = append(order, k)
			}
			if d.Since.Before(u.Since) {
				u.Since = d.Since
			}
			var det DriftDetail
			_ = json.Unmarshal(d.Detail, &det)
			if det.Running != "" && (d.Kind != "env" || u.Running == "") {
				u.Running = det.Running
			}
			if len(det.On) > 0 {
				u.On = det.On
			}
			switch d.Kind {
			case "upstream":
				u.Target, u.Jump = det.Other, det.Jump
			case "env":
				u.Behind = det.OtherIn
				if u.Target == "" {
					u.Target = det.Other
				}
				if u.Jump == JumpNone {
					u.Jump = det.Jump
				}
			case "eol":
				u.EOL = det.EOL
				if t, err := time.Parse("2006-01-02", det.EOL); err != nil || !t.After(now) {
					u.EOLPassed = true
				}
			}
		}
	}
	out := make([]Update, 0, len(order))
	for _, k := range order {
		u := byKey[k]
		if u.Target == "" { // end of life only: point at the newest acceptable release
			if up, ok := o.Upstreams[k.svc]; ok && up.HasLatest {
				switch {
				case !runsBelow(o, k.svc, k.app, k.env, up.Latest):
					// The newest release runs already: an end of life to come has nothing to move to yet, and one
					// passed needs a fork or a replacement, not this release.
					if !u.EOLPassed {
						continue
					}
				default:
					u.Target = up.Latest.Raw
				}
			}
		}
		if u.Target != "" {
			u.TargetURL = o.ReleaseURL[k.svc+"|"+u.Target]
			u.SecurityFix = securityUpTo(o, k.svc, u.Running, u.Target)
			u.SecuritySince = securitySince(o, k.svc, u.Running, u.Target)
			u.Breaking = breakingUpTo(o, k.svc, u.Running, u.Target)
			repo := o.Refs[k.svc].Repo
			if u.Path = UpgradePath(repo, u.Running, u.Target, o.Released[k.svc]); u.Path != nil {
				u.PathNote = StepNote(repo)
			}
		}
		u.Urgency = urgency(*u)
		u.Acked = ackedUpdate(*u, acks, now)
		out = append(out, *u)
	}
	sortUpdates(out)
	return out
}

// sortUpdates puts the most urgent first, production first within a level, then the oldest.
func sortUpdates(out []Update) {
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Urgency != b.Urgency {
			return a.Urgency < b.Urgency
		}
		if (len(a.Exposed) > 0) != (len(b.Exposed) > 0) {
			return len(a.Exposed) > 0
		}
		if a.Environment.Position != b.Environment.Position {
			return a.Environment.Position > b.Environment.Position // production first
		}
		if !a.Since.Equal(b.Since) {
			return a.Since.Before(b.Since)
		}
		if a.Service.Name != b.Service.Name {
			return a.Service.Name < b.Service.Name
		}
		return a.App < b.App
	})
}

// WithVulnerabilities adds what the running images are known to carry to each update and puts those with a
// vulnerability exploited in the wild first: upgrading is how most of them go away.
func WithVulnerabilities(us []Update, r VulnReport) []Update {
	type key struct{ svc, app, env string }
	exploited, critical, since := map[key][]string{}, map[key]int{}, map[key]time.Time{}
	hostFixes := map[key][]HostFix{}
	for _, f := range r.Findings {
		if f.Exploited != nil {
			for _, a := range f.Affected {
				if !a.Host {
					continue
				}
				k := key{a.ServiceID, a.App, a.EnvironmentID}
				h := HostFix{CVE: f.Key, Server: a.Server, Package: a.Package, Fix: a.Fix, FixNewer: a.FixNewer}
				if !slices.Contains(hostFixes[k], h) {
					hostFixes[k] = append(hostFixes[k], h)
				}
			}
		}
		seen := map[key]bool{}
		for _, a := range f.Affected {
			k := key{a.ServiceID, a.App, a.EnvironmentID}
			first := !seen[k]
			seen[k] = true
			switch {
			case f.Exploited != nil:
				// Counted once per line; known exploited there since the earliest of its images (the deadline runs
				// from it), whatever order the images come in.
				if first {
					exploited[k] = append(exploited[k], f.Key)
				}
				known := a.Since
				if added, err := time.Parse(time.DateOnly, f.Exploited.DateAdded); err == nil && added.After(known) {
					known = added
				}
				if s, ok := since[k]; !known.IsZero() && (!ok || known.Before(s)) {
					since[k] = known
				}
			case strings.EqualFold(f.Severity, "CRITICAL") && first:
				critical[k]++
			}
		}
	}
	for i := range us {
		k := key{us[i].Service.ID, us[i].App, us[i].Environment.ID}
		us[i].Exploited, us[i].Critical, us[i].ExploitedSince = exploited[k], critical[k], since[k]
		sort.Strings(us[i].Exploited)
		us[i].HostFixes = hostFixes[k]
		sort.Slice(us[i].HostFixes, func(a, b int) bool {
			x, y := us[i].HostFixes[a], us[i].HostFixes[b]
			if x.Server != y.Server {
				return x.Server < y.Server
			}
			if x.Package != y.Package {
				return x.Package < y.Package
			}
			return x.CVE < y.CVE
		})
		us[i].Urgency = urgency(us[i])
	}
	sortUpdates(us)
	return us
}

// WithExposure adds how each update's running version can be reached from outside, the patches its platform has
// waiting and what AWS charges for running it past the end of standard support, from what its workloads in that
// environment run, and puts exposed ones first within their urgency.
func WithExposure(us []Update, active []store.Instance) []Update {
	return withExposureAt(us, active, time.Now())
}

func withExposureAt(us []Update, active []store.Instance, now time.Time) []Update {
	type key struct{ svc, app, env string }
	split := SplitServices(active)
	ways, maint, servers := map[key][]string{}, map[key][]string{}, map[key][]string{}
	type engine struct {
		repo, version string
		vcpus         int
	}
	engines := map[key]map[string]*engine{} // the managed engines it runs, by repo and version
	for _, in := range active {
		if !in.IsMain {
			continue
		}
		k := key{in.ServiceID, lineApp(split, in), in.EnvironmentID}
		if in.WorkloadKind == "host_os" && in.RemovedAt.IsZero() && !slices.Contains(servers[k], in.TargetID) {
			servers[k] = append(servers[k], in.TargetID)
		}
		if ref := ParseImage(in.Image); Managed(ref.Repo()) {
			if engines[k] == nil {
				engines[k] = map[string]*engine{}
			}
			e := engines[k][in.Image]
			if e == nil {
				e = &engine{repo: ref.Repo(), version: ref.Tag}
				engines[k][in.Image] = e
			}
			e.vcpus += in.VCPUs
		}
		if len(in.Exposed) > 0 {
			ways[k] = union(ways[k], in.Exposed)
		}
		if len(in.Maintenance) > 0 {
			maint[k] = union(maint[k], in.Maintenance)
		}
	}
	for i := range us {
		k := key{us[i].Service.ID, us[i].App, us[i].Environment.ID}
		us[i].Exposed, us[i].Maintenance, us[i].Servers = ways[k], maint[k], servers[k]
		if us[i].EOL == "" {
			continue
		}
		var total *ExtendedSupport
		for _, e := range engines[k] {
			if e.version != us[i].Running {
				continue
			}
			es, ok := ExtendedSupportCost(e.repo, us[i].EOL, e.vcpus, now)
			if !ok {
				continue
			}
			if total == nil {
				total = &es
			} else { // the same engine and version on more than one target: the bills add up
				total.Monthly += es.Monthly
				total.Basis = ""
			}
		}
		us[i].ExtendedSupport = total
	}
	sortUpdates(us)
	return us
}

// LoadUpdates lists what to upgrade with the vulnerabilities of the running images; without a vulnerability
// report (OSV off, or it failed) the list is the same, ranked by drift alone.
func LoadUpdates(ctx context.Context, st *store.Store, sc store.Scope, o Overview, acks []store.Ack, now time.Time) []Update {
	return loadUpdates(ctx, st, sc, o, acks, now, false)
}

// LoadWork is LoadUpdates with what runs an exploited vulnerability and has no upgrade (WithUnpatched): the to-do
// list, as the Updates page and the API show it. Counts of what is behind a newer release use LoadUpdates.
func LoadWork(ctx context.Context, st *store.Store, sc store.Scope, o Overview, acks []store.Ack, now time.Time) []Update {
	return loadUpdates(ctx, st, sc, o, acks, now, true)
}

func loadUpdates(ctx context.Context, st *store.Store, sc store.Scope, o Overview, acks []store.Ack, now time.Time, unpatched bool) []Update {
	us := Updates(o, acks, now)
	r, rerr := LoadVulnReport(ctx, st, sc, "")
	if rerr == nil && unpatched {
		us = WithUnpatched(us, o, r, acks, now)
	}
	active, aerr := st.ListActiveInstances(ctx, sc)
	if aerr == nil {
		if unpatched {
			us = WithMaintenance(us, o, active, acks, now)
		}
		us = WithExposure(us, active)
	}
	if rerr == nil {
		us = WithVulnerabilities(us, r)
		if idx, err := loadVulnIndex(ctx, st, sc, r); err == nil {
			for i := range us {
				u := &us[i]
				if u.Target != "" && u.Target != u.Running {
					u.Vulns = idx.delta(vulnSide{u.Service.ID, "", u.Target}, vulnSide{u.Service.ID, u.Environment.ID, u.Running})
				}
			}
		}
		_ = releaseDeltas(ctx, st, sc, o, us) // the versions that run nowhere yet, from their release images
	}
	if r, err := st.Remediation(ctx, sc); err == nil && r.Any() {
		us = WithDeadlines(us, r)
	}
	if aerr == nil {
		us = WithRisk(us, active)
	}
	if troubles, err := Troubles(ctx, st, sc, o, now); err == nil {
		us = WithTroubles(us, o, troubles)
	}
	return us
}

func urgency(u Update) int {
	switch {
	case len(u.Exploited) > 0:
		return UrgencyExploited
	case u.SecurityFix != "":
		return UrgencySecurity
	case u.EOL != "" && u.EOLPassed:
		return UrgencyEOLPassed
	case u.EOL != "":
		return UrgencyEOLSoon
	case u.Jump == JumpMajor:
		return UrgencyMajor
	case u.Jump == JumpMinor:
		return UrgencyMinor
	case u.Behind != "":
		return UrgencyBehindEnv
	}
	return UrgencyPatch
}

// ackedUpdate reports whether a drift acknowledgement for the service (in this or
// every environment) is still in force: until a date, or until a version newer than
// the target appears.
func ackedUpdate(u Update, acks []store.Ack, now time.Time) bool {
	for _, a := range acks {
		if a.Kind != "drift" || a.ServiceID != u.Service.ID || (a.EnvironmentID != "" && a.EnvironmentID != u.Environment.ID) {
			continue
		}
		if !a.UntilAt.IsZero() && now.Before(a.UntilAt) {
			return true
		}
		if a.UntilVersion != "" {
			to, okTo := ParseVersion(u.Target)
			until, okUntil := ParseVersion(a.UntilVersion)
			if okTo && okUntil && to.Compare(until) < 0 {
				return true
			}
		}
	}
	return false
}

// securityUpTo gathers the security fixes the service's releases after running, up to and including target, name.
func securityUpTo(o Overview, svc, running, target string) string {
	from, ok1 := ParseVersion(running)
	to, ok2 := ParseVersion(target)
	if !ok1 || !ok2 {
		return ""
	}
	var ids []string
	found := false
	for key, sec := range o.ReleaseSecurity {
		id, ver, _ := strings.Cut(key, "|")
		if id != svc {
			continue
		}
		v, ok := ParseVersion(ver)
		if !ok || v.Compare(from) <= 0 || v.Compare(to) > 0 {
			continue
		}
		found = true
		if sec != "security" {
			ids = append(ids, strings.Split(sec, ", ")...)
		}
	}
	if !found {
		return ""
	}
	if len(ids) == 0 {
		return "security"
	}
	sort.Strings(ids)
	return strings.Join(slices.Compact(ids), ", ")
}

// WithUnpatched adds what runs an exploited vulnerability and has no upgrade on the list, its own image say, that no
// newer release is known of: what runs it in each environment, with what would fix it. Vulnerabilities mitigated
// there, or that the image's publisher or distribution says do not apply, are left out.
func WithUnpatched(us []Update, o Overview, r VulnReport, acks []store.Ack, now time.Time) []Update {
	type key struct{ svc, app, env string }
	listed := map[key]bool{}
	for _, u := range us {
		listed[key{u.Service.ID, u.App, u.Environment.ID}] = true
	}
	added := map[key]*Update{}
	var order []key
	for _, f := range r.Findings {
		if f.Exploited == nil {
			continue
		}
		for _, a := range f.Affected {
			k := key{a.ServiceID, a.App, a.EnvironmentID}
			if a.ServiceID == "" || a.EnvironmentID == "" || listed[k] || a.Mitigation != nil || a.Distro == DistroNotAffected || a.Distro == DistroUnimportant || a.Distro == DistroNegligible ||
				a.VEX != nil && (a.VEX.Status == "not_affected" || a.VEX.Status == "fixed") {
				continue
			}
			u := added[k]
			if u == nil {
				img := ParseImage(a.Image)
				u = &Update{Service: o.Services[a.ServiceID], App: a.App, Environment: o.Envs[a.EnvironmentID], Running: img.Tag, Since: a.Since}
				added[k] = u
				order = append(order, k)
			}
			if !a.Since.IsZero() && (u.Since.IsZero() || a.Since.Before(u.Since)) {
				u.Since = a.Since
			}
			if u.Remedy == "" || a.Rebuild != "" && !strings.HasPrefix(u.Remedy, "rebuild") {
				u.Remedy = remedy(a)
			}
		}
	}
	for _, k := range order {
		u := added[k]
		if u.Service.ID == "" {
			continue // a service gone since the report was built
		}
		u.Acked = ackedUpdate(*u, acks, now)
		us = append(us, *u)
	}
	return us
}

// WithMaintenance adds a line for each service and environment where the platform has something waiting (a host's
// reboot, a managed database's pending patches) and the list has nothing else: it is work to do, though no newer
// version is.
func WithMaintenance(us []Update, o Overview, active []store.Instance, acks []store.Ack, now time.Time) []Update {
	type key struct{ svc, app, env string }
	listed := map[key]bool{}
	for _, u := range us {
		listed[key{u.Service.ID, u.App, u.Environment.ID}] = true
	}
	split := SplitServices(active)
	added := map[key]*Update{}
	var order []key
	for _, in := range active {
		k := key{in.ServiceID, lineApp(split, in), in.EnvironmentID}
		if !in.IsMain || len(in.Maintenance) == 0 || in.ServiceID == "" || in.EnvironmentID == "" || listed[k] {
			continue
		}
		u := added[k]
		if u == nil {
			u = &Update{
				Service: o.Services[in.ServiceID], App: k.app, Environment: o.Envs[in.EnvironmentID], Running: ParseImage(in.Image).Tag,
				Since: now, Urgency: UrgencyPatch, MaintenanceOnly: true,
			}
			added[k] = u
			order = append(order, k)
		}
		u.Maintenance = union(u.Maintenance, in.Maintenance)
	}
	for _, k := range order {
		u := added[k]
		if u.Service.ID == "" {
			continue
		}
		u.Remedy = "apply the patches waiting at the platform"
		reboot := true
		for _, m := range u.Maintenance {
			reboot = reboot && strings.HasPrefix(m, "reboot required")
		}
		if reboot {
			u.Remedy = "reboot"
		}
		u.Acked = ackedUpdate(*u, acks, now)
		us = append(us, *u)
	}
	sortUpdates(us)
	return us
}

// remedy says what fixes a vulnerability where it runs, when no newer release of the service is known.
func remedy(a Affected) string {
	switch {
	case a.Rebuild != "":
		return "rebuild on the current " + a.Rebuild
	case a.Newer != nil:
		name := a.Newer.Distro + " " + a.Newer.Release
		if a.Newer.Name != "" {
			name += " (" + a.Newer.Name + ")"
		}
		return "a base on " + name
	case a.Fix != "":
		return a.Package + " " + a.Fix
	case a.FixNewer:
		name, _ := splitPackage(a.Package)
		return "a newer " + name
	case a.EOLOS != "":
		return "a base on a supported release: " + a.EOLOS + " is past its end of life and gets no fixes"
	}
	return "no fix known yet"
}

// runsBelow reports whether anything a service runs in an environment (within app, when the service is split by
// application) is older than v, or does not read as a version.
func runsBelow(o Overview, svc, app, env string, v Version) bool {
	ei := -1
	for i, e := range o.Matrix.Environments {
		if e.ID == env {
			ei = i
		}
	}
	for _, row := range o.Matrix.Rows {
		if row.Service.ID != svc || ei < 0 {
			continue
		}
		for _, part := range row.Units() {
			if part.App != app || ei >= len(part.Cells) {
				continue
			}
			if len(part.Cells[ei].Versions) == 0 {
				return true
			}
			for _, rv := range part.Cells[ei].Versions {
				if pv, ok := ParseVersion(rv.Version()); !ok || pv.Compare(v) < 0 {
					return true
				}
			}
			return false
		}
	}
	return true
}

// FirstStop is the version an upgrade of a service from running to target goes to first: target, or the first
// stop of its upgrade path.
func FirstStop(o Overview, svc, running, target string) string {
	if p := UpgradePath(o.Refs[svc].Repo, running, target, o.Released[svc]); len(p) > 0 {
		return p[0]
	}
	return target
}

// BreakingUpTo is what the release notes from running up to target say breaks, quoted: "2.0.0: Removed …".
func BreakingUpTo(o Overview, svc, running, target string) []string {
	return breakingUpTo(o, svc, running, target)
}
