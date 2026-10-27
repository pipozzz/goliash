// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package versions

import (
	"context"
	"sort"
	"time"

	"github.com/pipozzz/goliash/internal/store"
)

// The vulnerability management report is one month of a workspace's patching, as evidence for an audit (NIS2,
// ISO 27001 A.8.8, SOC 2, DORA): the remediation policy in force, production's posture at the month's end, the
// upgrades deployed during it and whether security fixes met their deadline, each team, and what is open and
// accepted as the report is made.

// VMReport is the vulnerability management report of one month.
type VMReport struct {
	From, To  time.Time // the month: From inclusive, To exclusive
	Prod      store.Environment
	Policy    store.Remediation
	MonthEnd  *store.PostureSample // production's posture on the month's last recorded day; nil before recording began
	Upgrades  []VMUpgrade          // deployed to production during the month, oldest first
	Security  int                  // of them, upgrades that brought a release fixing vulnerabilities
	InTime    int                  // of those, deployed within the security-fix deadline (when one is set)
	Median    int                  // days to patch, over the month's upgrades
	P90       int
	Teams     []VMTeam // per team (the services' owner), most upgrades first
	Exploited []VulnFinding
	Overdue   []Update
	Accepted  []VMAccepted
	// Mitigations of exploited vulnerabilities with no fix: in force, or closed during or after the month.
	Mitigations []VMMitigation
}

// VMMitigation is a mitigation, with the names it applies to.
type VMMitigation struct {
	store.Mitigation
	Service, Environment string
}

// VMAccepted is a risk accepted as the report is made: an acknowledgement, with the names it applies to.
type VMAccepted struct {
	store.Ack
	Service, Environment string // Environment is "" for every environment
}

// VMUpgrade is one upgrade deployed to production, and whether it fixed vulnerabilities in time.
type VMUpgrade struct {
	PatchSample
	SecurityFix string // what the releases on the way fixed (CVE ids, or "security"); "" when nothing known
	InTime      *bool  // within the security-fix deadline; nil when not a security fix or no deadline is set
}

// VMTeam is one team's month.
type VMTeam struct {
	Owner            string
	Upgrades, Median int
	Security, InTime int
}

// SLAPercent is the share of security fixes deployed within their deadline, 0–100; -1 when there were none or no
// deadline is set.
func (r VMReport) SLAPercent() int {
	if r.Security == 0 || r.Policy.Security == 0 {
		return -1
	}
	return r.InTime * 100 / r.Security
}

// LoadVMReport builds the report of the month starting at from.
func LoadVMReport(ctx context.Context, st *store.Store, sc store.Scope, from, now time.Time) (VMReport, error) {
	r := VMReport{From: from, To: from.AddDate(0, 1, 0)}
	o, err := LoadOverview(ctx, st, sc)
	if err != nil {
		return r, err
	}
	pos, err := LoadPosture(ctx, st, sc, o, now)
	if err != nil {
		return r, err
	}
	r.Prod = pos.Prod
	if r.Policy, err = st.Remediation(ctx, sc); err != nil {
		return r, err
	}
	hist, err := st.PostureHistory(ctx, sc, from.Format(time.DateOnly))
	if err != nil {
		return r, err
	}
	last := r.To.Format(time.DateOnly)
	for i := range hist {
		if hist[i].Day < last {
			s := hist[i]
			r.MonthEnd = &s
		}
	}

	samples, err := PatchTimes(ctx, st, sc, o, pos.Prod.ID, from)
	if err != nil {
		return r, err
	}
	// What each release fixes, for every service: the overview reads it only for services with an upstream.
	all, err := releasesByService(ctx, st, sc)
	if err != nil {
		return r, err
	}
	for id, rels := range all {
		for _, rel := range rels {
			if rel.Security != "" {
				o.ReleaseSecurity[id+"|"+rel.Version] = rel.Security
			}
		}
	}
	teams := map[string]*VMTeam{}
	teamDays := map[string][]int{}
	var days []int
	for _, s := range samples {
		if !s.At.Before(r.To) {
			continue
		}
		u := VMUpgrade{PatchSample: s, SecurityFix: securityUpTo(o, s.ServiceID, s.From, s.To)}
		t := teams[s.Owner]
		if t == nil {
			t = &VMTeam{Owner: s.Owner}
			teams[s.Owner] = t
		}
		t.Upgrades++
		teamDays[s.Owner] = append(teamDays[s.Owner], s.Days)
		days = append(days, s.Days)
		if u.SecurityFix != "" {
			r.Security++
			t.Security++
			if r.Policy.Security > 0 {
				ok := s.Days <= r.Policy.Security
				u.InTime = &ok
				if ok {
					r.InTime++
					t.InTime++
				}
			}
		}
		r.Upgrades = append(r.Upgrades, u)
	}
	r.Median, r.P90 = percentile(days, 50), percentile(days, 90)
	for owner, t := range teams {
		t.Median = percentile(teamDays[owner], 50)
		r.Teams = append(r.Teams, *t)
	}
	sort.Slice(r.Teams, func(i, j int) bool {
		if r.Teams[i].Upgrades != r.Teams[j].Upgrades {
			return r.Teams[i].Upgrades > r.Teams[j].Upgrades
		}
		return r.Teams[i].Owner < r.Teams[j].Owner
	})

	// What is open and accepted as the report is made.
	if vulns, err := LoadVulnReport(ctx, st, sc, pos.Prod.ID); err == nil {
		for _, f := range vulns.Findings {
			if f.Exploited != nil {
				r.Exploited = append(r.Exploited, f)
			}
		}
	}
	acks, err := st.ListAcks(ctx, sc)
	if err != nil {
		return r, err
	}
	for _, u := range LoadUpdates(ctx, st, sc, o, acks, now) {
		if !u.Acked && u.Overdue(now) && u.Environment.ID == pos.Prod.ID {
			r.Overdue = append(r.Overdue, u)
		}
	}
	if ms, err := st.ListMitigationsSince(ctx, sc, from); err == nil {
		for _, m := range ms {
			r.Mitigations = append(r.Mitigations, VMMitigation{Mitigation: m, Service: o.Services[m.ServiceID].Name, Environment: o.Envs[m.EnvironmentID].Name})
		}
	}
	for _, a := range acks {
		if a.UntilAt.IsZero() || a.UntilAt.After(now) {
			r.Accepted = append(r.Accepted, VMAccepted{Ack: a, Service: o.Services[a.ServiceID].Name, Environment: o.Envs[a.EnvironmentID].Name})
		}
	}
	return r, nil
}
