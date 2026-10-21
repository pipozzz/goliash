// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package versions

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/pipozzz/goliash/internal/store"
)

// HealthCheck is one part of Goliash's own health: whether it works and its data is fresh.
type HealthCheck struct {
	Title    string   `json:"title"`
	State    string   `json:"state"` // ok, warn, bad or off
	Summary  string   `json:"summary"`
	Details  []string `json:"details,omitempty"`
	Link     string   `json:"link,omitempty"`
	LinkText string   `json:"-"`
}

// Health is Goliash's own health for a workspace: can its numbers be trusted right now?
type Health struct {
	Checks  []HealthCheck `json:"checks"`
	Worst   string        `json:"state"` // the worst state of the checks
	Checked time.Time     `json:"checked_at"`
}

// healthDetailsShown caps the lines under a check.
const healthDetailsShown = 6

// LoadHealth checks Goliash's own health for a workspace: whether the data on every page can be trusted right
// now. checker may be nil; version is the server's release, and updateCheck lets it ask for a newer one.
func LoadHealth(ctx context.Context, st *store.Store, checker *Checker, sc store.Scope, version string, updateCheck bool) (Health, error) {
	now := time.Now()
	v := Health{Checked: now}
	add := func(c HealthCheck) {
		if len(c.Details) > healthDetailsShown {
			more := len(c.Details) - healthDetailsShown
			c.Details = append(c.Details[:healthDetailsShown], fmt.Sprintf("and %d more", more))
		}
		v.Checks = append(v.Checks, c)
	}

	// Agents: silent ones mean old data.
	agents, err := st.ListAgents(ctx, sc)
	if err != nil {
		return v, err
	}
	c := HealthCheck{Title: "Agents", State: "ok", Link: "/agents", LinkText: "Agents and targets"}
	silent, older := 0, 0
	server, serverKnown := ParseVersion(strings.TrimPrefix(version, "v"))
	for _, a := range agents {
		if a.ActiveTokens == 0 {
			continue
		}
		if !a.StaleSince.IsZero() {
			silent++
			c.Details = append(c.Details, fmt.Sprintf("%s: silent since %s", a.Name, healthAgo(now, a.StaleSince)))
		}
		// An agent left on an older release misses its fixes, security ones among them.
		if av, ok := ParseVersion(strings.TrimPrefix(a.Version, "v")); serverKnown && ok && av.Compare(server) < 0 {
			older++
			c.Details = append(c.Details, fmt.Sprintf("%s: runs %s, the server %s", a.Name, a.Version, version))
		}
	}
	switch {
	case len(agents) == 0:
		c.State, c.Summary = "off", "No agents. Targets the server collects itself still report."
	case silent > 0:
		c.State, c.Summary = "bad", fmt.Sprintf("%d of %d silent: what they watch may be out of date.", silent, len(agents))
	case older > 0:
		c.State, c.Summary = "warn", fmt.Sprintf("%d of %d on an older release than the server: upgrade them (their pages show the command).", older, len(agents))
	default:
		c.Summary = healthPlural(len(agents), "agent reports", "agents report") + " in time."
	}
	add(c)

	// Targets without a recent snapshot.
	o, err := LoadOverview(ctx, st, sc)
	if err != nil {
		return v, err
	}
	c = HealthCheck{Title: "Targets", State: "ok", Link: "/agents", LinkText: "Targets"}
	for _, t := range o.Targets {
		if at, stale := o.Stale[t.ID]; stale {
			c.Details = append(c.Details, fmt.Sprintf("%s: last snapshot %s", t.Name, healthAgoOrNever(now, at)))
		}
	}
	sort.Strings(c.Details)
	switch {
	case len(o.Targets) == 0:
		c.State, c.Summary = "off", "No targets yet."
	case len(c.Details) > 0:
		c.State, c.Summary = "warn", fmt.Sprintf("%d of %d have no recent snapshot.", len(c.Details), len(o.Targets))
	default:
		c.Summary = healthPlural(len(o.Targets), "target is", "targets are") + " current."
	}
	add(c)

	// Upstream checks: when they ran and which failed.
	c = HealthCheck{Title: "Upstream releases", State: "ok", Link: "/updates", LinkText: "Updates"}
	var last time.Time
	checked, never := 0, 0
	checks, err := st.UpstreamChecks(ctx, sc)
	if err != nil {
		return v, err
	}
	for id, ref := range o.Refs {
		if ref.Repo == "" || PackageRepo(ref.Repo) || CheckedByAgent(o.Services[id], ref.Repo, o.Registries) {
			continue
		}
		at, msg := checks[id].At, checks[id].Error
		if at.IsZero() {
			never++
			continue
		}
		checked++
		if at.After(last) {
			last = at
		}
		if msg != "" {
			c.Details = append(c.Details, o.Services[id].Name+": "+msg)
		}
	}
	sort.Strings(c.Details)
	switch {
	case checked == 0 && never == 0:
		c.State, c.Summary = "off", "No service with an image the server checks."
	case len(c.Details) > 0:
		c.State, c.Summary = "warn", fmt.Sprintf("%d of %d checks failed the last time; last check %s.", len(c.Details), checked, healthAgoOrNever(now, last))
	case checked == 0:
		c.State, c.Summary = "warn", fmt.Sprintf("%s not checked yet.", healthPlural(never, "service is", "services are"))
	default:
		c.Summary = fmt.Sprintf("%s checked; last check %s.", healthPlural(checked, "service", "services"), healthAgo(now, last))
		if now.Sub(last) > 3*time.Hour {
			c.State, c.Summary = "warn", fmt.Sprintf("No check for %s: is the server's network out?", healthAgo(now, last))
		}
	}
	add(c)

	// Private registries the server reads.
	if creds, err := st.ListRegistryCredentials(ctx, sc); err == nil && len(creds) > 0 {
		c = HealthCheck{Title: "Private registries", State: "ok", Link: "/settings#registries", LinkText: "Private registries"}
		for _, cr := range creds {
			if cr.Error != "" {
				c.Details = append(c.Details, cr.Host+": "+cr.Error)
			}
		}
		if len(c.Details) > 0 {
			c.State, c.Summary = "bad", healthPlural(len(c.Details), "registry refuses", "registries refuse")+" the credentials Goliash has."
		} else {
			c.Summary = healthPlural(len(creds), "registry accepts", "registries accept") + " its credentials."
		}
		add(c)
	}

	// Vulnerability data: SBOMs read, OSV answering, CISA's catalog current.
	c = HealthCheck{Title: "Vulnerability data", State: "ok", Link: "/security/vulns", LinkText: "Is it running?"}
	if checker == nil || !checker.OSVEnabled() {
		c.State, c.Summary = "off", "OSV lookups are off (GOLIASH_OSV_URL=off)."
	} else if rep, err := LoadVulnReport(ctx, st, sc, ""); err == nil {
		osvAt, _ := st.OSVCheckedAt(ctx)
		_, kevAt, _ := st.KnownExploited(ctx)
		c.Summary = fmt.Sprintf("SBOMs of %d of %d running images read; OSV last answered %s.", rep.WithSBOM, rep.Images, healthAgoOrNever(now, osvAt))
		if rep.Unasked > 0 || rep.Pending > 0 {
			c.State = "warn"
			var parts []string
			if rep.Unasked > 0 {
				parts = append(parts, healthPlural(rep.Unasked, "package", "packages"))
			}
			if rep.Pending > 0 {
				parts = append(parts, healthPlural(rep.Pending, "advisory", "advisories"))
			}
			c.Details = append(c.Details, strings.Join(parts, " and ")+" still to look up: answers may be incomplete.")
		}
		if !osvAt.IsZero() && now.Sub(osvAt) > 48*time.Hour {
			c.State = "warn"
			c.Details = append(c.Details, "OSV has not answered for "+healthAgo(now, osvAt)+".")
		}
		if kevAt.IsZero() {
			c.Details = append(c.Details, "CISA's catalog of exploited vulnerabilities is not read yet.")
		} else {
			c.Details = append(c.Details, "CISA's catalog of exploited vulnerabilities read "+healthAgo(now, kevAt)+".")
			if now.Sub(kevAt) > 72*time.Hour {
				c.State = "warn"
			}
		}
		if sboms, err := st.ListImageSBOMs(ctx, sc); err == nil {
			failed := 0
			for _, b := range sboms {
				if b.Error != "" {
					failed++
				}
			}
			if failed > 0 {
				c.Details = append(c.Details, healthPlural(failed, "SBOM", "SBOMs")+" could not be read; Goliash tries again daily.")
			}
		}
	}
	add(c)

	// Notifications that do not go out.
	if dels, err := st.RecentDeliveries(ctx, sc, 100); err == nil {
		c = HealthCheck{Title: "Notifications", State: "ok", Link: "/notifications", LinkText: "Notifications"}
		sent, failing := 0, map[string]string{}
		for _, d := range dels {
			if d.SentAt != nil {
				sent++
			} else if d.LastError != "" && d.Attempts > 0 {
				failing[d.ChannelName] = d.LastError
			}
		}
		for ch, msg := range failing {
			c.Details = append(c.Details, ch+": "+msg)
		}
		sort.Strings(c.Details)
		switch {
		case len(dels) == 0:
			c.State, c.Summary = "off", "Nothing sent yet."
		case len(failing) > 0:
			c.State, c.Summary = "bad", healthPlural(len(failing), "channel fails", "channels fail")+" to deliver."
		default:
			c.Summary = fmt.Sprintf("The last %d deliveries went out.", sent)
		}
		add(c)
	}

	// Goliash itself: a newer release, from its own registry.
	c = HealthCheck{Title: "Goliash", State: "ok", Link: "https://github.com/pipozzz/goliash/releases", LinkText: "Releases"}
	switch {
	case !updateCheck:
		c.State, c.Summary = "off", "Version "+version+"; looking for newer releases is off (GOLIASH_UPDATE_CHECK=off)."
	case checker == nil:
		c.Summary = "Version " + version + "."
	default:
		look, cancel := context.WithTimeout(ctx, 5*time.Second)
		newer, err := checker.NewerGoliash(look, version)
		cancel()
		switch {
		case err != nil:
			c.Summary = "Version " + version + "; could not look for a newer one: " + err.Error()
		case newer != "":
			c.State, c.Summary = "warn", "Version "+version+"; "+newer+" is out."
			c.Link, c.LinkText = "https://github.com/pipozzz/goliash/releases/tag/v"+newer, "Release notes"
			look, cancel := context.WithTimeout(ctx, 5*time.Second)
			if fixes := checker.GoliashSecurityFixes(look, version, newer); len(fixes) > 0 {
				c.State, c.Summary = "bad", "Version "+version+"; "+newer+" is out with security fixes."
				c.Details = append(c.Details, fixes[:min(len(fixes), 3)]...)
			}
			cancel()
			c.Details = append(c.Details, "Upgrade the server first, then the agents (their pages show the command).")
		default:
			c.Summary = "Version " + version + ", the newest."
		}
	}
	add(c)

	v.Worst = "ok"
	rank := map[string]int{"bad": 3, "warn": 2, "ok": 1, "off": 0}
	for _, c := range v.Checks {
		if rank[c.State] > rank[v.Worst] {
			v.Worst = c.State
		}
	}
	return v, nil
}

func healthAgo(now, t time.Time) string {
	d := now.Sub(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return healthPlural(int(d.Minutes()), "minute", "minutes") + " ago"
	case d < 48*time.Hour:
		return healthPlural(int(d.Hours()), "hour", "hours") + " ago"
	}
	return healthPlural(int(d.Hours()/24), "day", "days") + " ago"
}

func healthAgoOrNever(now, t time.Time) string {
	if t.IsZero() {
		return "never"
	}
	return healthAgo(now, t)
}

func healthPlural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}
