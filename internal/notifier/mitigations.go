// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package notifier

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/pipozzz/goliash/internal/store"
	"github.com/pipozzz/goliash/internal/versions"
)

// mitigationReviewDays is how many days before its review date a mitigation is brought up.
const mitigationReviewDays = 3

// checkMitigations keeps the mitigations of vulnerabilities with no fix in step with what runs: it closes those
// whose fix is out (saying so: now it is an upgrade) or whose image no longer runs, and, when a rule wants
// remediation deadlines, says where an exploited vulnerability with no fix runs in production without a
// mitigation, and which mitigation is due for review.
func (n *Notifier) checkMitigations(ctx context.Context, sc store.Scope, rules []store.Rule, now time.Time) error {
	open, err := n.store.ListOpenMitigations(ctx, sc)
	if err != nil {
		return err
	}
	rep, err := versions.LoadVulnReport(ctx, n.store, sc, "")
	if err != nil {
		return err
	}
	rem, _ := n.store.Remediation(ctx, sc)
	nm, err := n.loadNames(ctx, sc)
	if err != nil {
		return err
	}
	alerts := anyRuleFor(rules, EventDeadline)
	item := func(svcID, envID, note string) Item {
		svc := nm.services[svcID]
		it := Item{
			Type: EventDeadline, Service: svc.Name, Owner: svc.Owner, Environment: nm.envs[envID], Production: envID == nm.prod,
			At: now, Note: note, URL: "/security/vulns?kev=1",
		}
		it.Text = Describe(it)
		return it
	}

	// Close what is no longer needed.
	for _, m := range open {
		f, ok := findingOf(rep, m.CVE)
		runs := ok && slices.ContainsFunc(f.Affected, func(a versions.Affected) bool {
			return a.ServiceID == m.ServiceID && a.EnvironmentID == m.EnvironmentID
		})
		reason := ""
		switch {
		case !runs:
			reason = "no longer runs"
		case f.Fixable:
			reason = "a fix is out"
		default:
			continue
		}
		if err := n.store.CloseMitigation(ctx, sc, m.ID, reason); err != nil {
			return err
		}
		if reason == "a fix is out" && alerts {
			fix := ""
			for _, a := range f.Affected {
				if a.ServiceID == m.ServiceID && a.EnvironmentID == m.EnvironmentID && a.Fix != "" {
					fix = a.Package + " " + a.Fix
				}
			}
			if err := n.enqueue(ctx, sc, rules, item(m.ServiceID, m.EnvironmentID, "fixed|"+m.CVE+"|"+fix), "mit|fixed|"+m.ID); err != nil {
				return err
			}
		}
	}
	if !alerts {
		return nil
	}

	// Production running an exploited vulnerability with no fix and no mitigation: once each.
	for _, f := range rep.Findings {
		due := f.MitigateBy(rem.Exploited)
		for _, a := range f.Unmitigated() {
			if a.EnvironmentID != nm.prod {
				continue
			}
			when := ""
			if !due.IsZero() {
				when = due.Format(time.DateOnly)
			}
			if err := n.enqueue(ctx, sc, rules, item(a.ServiceID, a.EnvironmentID, "unmitigated|"+f.Key+"|"+when),
				"mit|unmitigated|"+f.Key+"|"+a.ServiceID+"|"+a.EnvironmentID); err != nil {
				return err
			}
		}
	}
	// Mitigations due for review.
	for _, m := range open {
		left := m.ReviewAt.Sub(now)
		if left < 0 || left > mitigationReviewDays*24*time.Hour {
			continue
		}
		note := "review|" + m.CVE + "|" + m.ReviewAt.Format(time.DateOnly) + "|" + m.Measure
		if err := n.enqueue(ctx, sc, rules, item(m.ServiceID, m.EnvironmentID, note), "mit|review|"+m.ID+"|"+m.ReviewAt.Format(time.DateOnly)); err != nil {
			return err
		}
	}
	return nil
}

// findingOf is the finding of a CVE (by its key or an alias).
func findingOf(r versions.VulnReport, cve string) (versions.VulnFinding, bool) {
	for _, f := range r.Findings {
		if f.Key == cve || slices.Contains(f.Aliases, cve) {
			return f, true
		}
	}
	return versions.VulnFinding{}, false
}

// describeMitigation is a mitigation item in words, or "" when the note is about a deadline.
func describeMitigation(where string, it Item) string {
	state, rest, _ := strings.Cut(it.Note, "|")
	cve, rest, _ := strings.Cut(rest, "|")
	switch state {
	case "unmitigated":
		by := ""
		if rest != "" {
			if d, err := time.Parse(time.DateOnly, rest); err == nil {
				by = " by " + d.Format("2 Jan")
			}
		}
		return fmt.Sprintf("%s: %s is exploited in the wild and has no fix yet; record a mitigation%s (WAF rule, feature off, isolation)", where, cve, by)
	case "review":
		date, measure, _ := strings.Cut(rest, "|")
		when := date
		if d, err := time.Parse(time.DateOnly, date); err == nil {
			when = d.Format("2 Jan")
		}
		return fmt.Sprintf("%s: the mitigation of %s is due for review on %s: %s", where, cve, when, measure)
	case "fixed":
		fix := ""
		if rest != "" {
			fix = " (" + rest + ")"
		}
		return fmt.Sprintf("%s: a fix for %s is out%s; its mitigation is closed, upgrade now", where, cve, fix)
	}
	return ""
}
