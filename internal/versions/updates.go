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
	Breaking  []string
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
				u.Target = up.Latest.Raw
			}
		}
		if u.Target != "" {
			u.TargetURL = o.ReleaseURL[k.svc+"|"+u.Target]
			u.SecurityFix = securityUpTo(o, k.svc, u.Running, u.Target)
			u.SecuritySince = securitySince(o, k.svc, u.Running, u.Target)
			u.Breaking = breakingUpTo(o, k.svc, u.Running, u.Target)
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
	type key struct{ svc, env string }
	exploited, critical, since := map[key][]string{}, map[key]int{}, map[key]time.Time{}
	for _, f := range r.Findings {
		seen := map[key]bool{}
		for _, a := range f.Affected {
			k := key{a.ServiceID, a.EnvironmentID}
			if seen[k] {
				continue
			}
			seen[k] = true
			switch {
			case f.Exploited != nil:
				exploited[k] = append(exploited[k], f.Key)
				known := a.Since
				if added, err := time.Parse(time.DateOnly, f.Exploited.DateAdded); err == nil && added.After(known) {
					known = added
				}
				if s, ok := since[k]; !known.IsZero() && (!ok || known.Before(s)) {
					since[k] = known
				}
			case strings.EqualFold(f.Severity, "CRITICAL"):
				critical[k]++
			}
		}
	}
	for i := range us {
		k := key{us[i].Service.ID, us[i].Environment.ID}
		us[i].Exploited, us[i].Critical, us[i].ExploitedSince = exploited[k], critical[k], since[k]
		sort.Strings(us[i].Exploited)
		us[i].Urgency = urgency(us[i])
	}
	sortUpdates(us)
	return us
}

// WithExposure adds how each update's running version can be reached from outside, from what its workloads in
// that environment run, and puts exposed ones first within their urgency.
func WithExposure(us []Update, active []store.Instance) []Update {
	type key struct{ svc, env string }
	ways := map[key][]string{}
	for _, in := range active {
		if in.IsMain && len(in.Exposed) > 0 {
			k := key{in.ServiceID, in.EnvironmentID}
			ways[k] = union(ways[k], in.Exposed)
		}
	}
	for i := range us {
		us[i].Exposed = ways[key{us[i].Service.ID, us[i].Environment.ID}]
	}
	sortUpdates(us)
	return us
}

// LoadUpdates lists what to upgrade with the vulnerabilities of the running images; without a vulnerability
// report (OSV off, or it failed) the list is the same, ranked by drift alone.
func LoadUpdates(ctx context.Context, st *store.Store, sc store.Scope, o Overview, acks []store.Ack, now time.Time) []Update {
	us := Updates(o, acks, now)
	if active, err := st.ListActiveInstances(ctx, sc); err == nil {
		us = WithExposure(us, active)
	}
	if r, err := LoadVulnReport(ctx, st, sc, ""); err == nil {
		us = WithVulnerabilities(us, r)
		if idx, err := loadVulnIndex(ctx, st, sc, r); err == nil {
			for i := range us {
				u := &us[i]
				if u.Target != "" && u.Target != u.Running {
					u.Vulns = idx.delta(vulnSide{u.Service.ID, "", u.Target}, vulnSide{u.Service.ID, u.Environment.ID, u.Running})
				}
			}
		}
	}
	if r, err := st.Remediation(ctx, sc); err == nil && r.Any() {
		us = WithDeadlines(us, r)
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
