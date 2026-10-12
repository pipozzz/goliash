// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package ui

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"time"

	"github.com/pipozzz/goliash/internal/auth"
	"github.com/pipozzz/goliash/internal/versions"
)

// StatusCheck is one line of the status page: whether a part of Goliash works and its data is fresh.
type StatusCheck struct {
	Title, State, Summary, Link, LinkText string // State: ok, warn, bad or off
	Details                               []string
}

// StatusView is Goliash's own health for the workspace: can its numbers be trusted right now?
type StatusView struct {
	Base
	Checks  []StatusCheck
	Worst   string
	Checked time.Time
}

// statusDetailsShown caps the lines under a check.
const statusDetailsShown = 6

func (s *Server) status(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	v, err := s.statusView(r.Context(), p)
	if err != nil {
		return err
	}
	v.Base = withFlash(s.base(r.Context(), p, "status", "Status"), r)
	return render(w, r, StatusPage(v))
}

func (s *Server) statusView(ctx context.Context, p auth.Principal) (StatusView, error) {
	now := time.Now()
	v := StatusView{Checked: now}
	sc := p.Scope
	add := func(c StatusCheck) {
		if len(c.Details) > statusDetailsShown {
			more := len(c.Details) - statusDetailsShown
			c.Details = append(c.Details[:statusDetailsShown], fmt.Sprintf("and %d more", more))
		}
		v.Checks = append(v.Checks, c)
	}

	// Agents: silent ones mean old data.
	agents, err := s.store.ListAgents(ctx, sc)
	if err != nil {
		return v, err
	}
	c := StatusCheck{Title: "Agents", State: "ok", Link: "/agents", LinkText: "Agents and targets"}
	silent := 0
	for _, a := range agents {
		if a.ActiveTokens == 0 {
			continue
		}
		if !a.StaleSince.IsZero() {
			silent++
			c.Details = append(c.Details, fmt.Sprintf("%s: silent since %s", a.Name, ago(now, a.StaleSince)))
		}
	}
	switch {
	case len(agents) == 0:
		c.State, c.Summary = "off", "No agents. Targets the server collects itself still report."
	case silent > 0:
		c.State, c.Summary = "bad", fmt.Sprintf("%d of %d silent: what they watch may be out of date.", silent, len(agents))
	default:
		c.Summary = plural(len(agents), "agent reports", "agents report") + " in time."
	}
	add(c)

	// Targets without a recent snapshot.
	o, err := versions.LoadOverview(ctx, s.store, sc)
	if err != nil {
		return v, err
	}
	c = StatusCheck{Title: "Targets", State: "ok", Link: "/agents", LinkText: "Targets"}
	for _, t := range o.Targets {
		if at, stale := o.Stale[t.ID]; stale {
			c.Details = append(c.Details, fmt.Sprintf("%s: last snapshot %s", t.Name, agoOrNever(now, at)))
		}
	}
	sort.Strings(c.Details)
	switch {
	case len(o.Targets) == 0:
		c.State, c.Summary = "off", "No targets yet."
	case len(c.Details) > 0:
		c.State, c.Summary = "warn", fmt.Sprintf("%d of %d have no recent snapshot.", len(c.Details), len(o.Targets))
	default:
		c.Summary = plural(len(o.Targets), "target is", "targets are") + " current."
	}
	add(c)

	// Upstream checks: when they ran and which failed.
	c = StatusCheck{Title: "Upstream releases", State: "ok", Link: "/updates", LinkText: "Updates"}
	var last time.Time
	checked, never := 0, 0
	for id, ref := range o.Refs {
		if ref.Repo == "" || versions.PackageRepo(ref.Repo) || versions.CheckedByAgent(o.Services[id], ref.Repo, o.Registries) {
			continue
		}
		at, msg, err := s.store.UpstreamStatus(ctx, sc, id)
		if err != nil {
			continue
		}
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
		c.State, c.Summary = "warn", fmt.Sprintf("%d of %d checks failed the last time; last check %s.", len(c.Details), checked, agoOrNever(now, last))
	case checked == 0:
		c.State, c.Summary = "warn", fmt.Sprintf("%s not checked yet.", plural(never, "service is", "services are"))
	default:
		c.Summary = fmt.Sprintf("%s checked; last check %s.", plural(checked, "service", "services"), ago(now, last))
		if now.Sub(last) > 3*time.Hour {
			c.State, c.Summary = "warn", fmt.Sprintf("No check for %s: is the server's network out?", ago(now, last))
		}
	}
	add(c)

	// Private registries the server reads.
	if creds, err := s.store.ListRegistryCredentials(ctx, sc); err == nil && len(creds) > 0 {
		c = StatusCheck{Title: "Private registries", State: "ok", Link: "/settings#registries", LinkText: "Private registries"}
		for _, cr := range creds {
			if cr.Error != "" {
				c.Details = append(c.Details, cr.Host+": "+cr.Error)
			}
		}
		if len(c.Details) > 0 {
			c.State, c.Summary = "bad", plural(len(c.Details), "registry refuses", "registries refuse")+" the credentials Goliash has."
		} else {
			c.Summary = plural(len(creds), "registry accepts", "registries accept") + " its credentials."
		}
		add(c)
	}

	// Vulnerability data: SBOMs read, OSV answering, CISA's catalog current.
	c = StatusCheck{Title: "Vulnerability data", State: "ok", Link: "/security/vulns", LinkText: "Is it running?"}
	if s.checker == nil || !s.checker.OSVEnabled() {
		c.State, c.Summary = "off", "OSV lookups are off (GOLIASH_OSV_URL=off)."
	} else if rep, err := versions.LoadVulnReport(ctx, s.store, sc, ""); err == nil {
		osvAt, _ := s.store.OSVCheckedAt(ctx)
		_, kevAt, _ := s.store.KnownExploited(ctx)
		c.Summary = fmt.Sprintf("SBOMs of %d of %d running images read; OSV last answered %s.", rep.WithSBOM, rep.Images, agoOrNever(now, osvAt))
		if rep.Unasked > 0 || rep.Pending > 0 {
			c.State = "warn"
			c.Details = append(c.Details, fmt.Sprintf("%s and %s still to look up: answers may be incomplete.",
				plural(rep.Unasked, "package", "packages"), plural(rep.Pending, "advisory", "advisories")))
		}
		if !osvAt.IsZero() && now.Sub(osvAt) > 48*time.Hour {
			c.State = "warn"
			c.Details = append(c.Details, "OSV has not answered for "+ago(now, osvAt)+".")
		}
		if kevAt.IsZero() {
			c.Details = append(c.Details, "CISA's catalog of exploited vulnerabilities is not read yet.")
		} else {
			c.Details = append(c.Details, "CISA's catalog of exploited vulnerabilities read "+ago(now, kevAt)+".")
			if now.Sub(kevAt) > 72*time.Hour {
				c.State = "warn"
			}
		}
		if sboms, err := s.store.ListImageSBOMs(ctx, sc); err == nil {
			failed := 0
			for _, b := range sboms {
				if b.Error != "" {
					failed++
				}
			}
			if failed > 0 {
				c.Details = append(c.Details, plural(failed, "SBOM", "SBOMs")+" could not be read; Goliash tries again daily.")
			}
		}
	}
	add(c)

	// Notifications that do not go out.
	if dels, err := s.store.RecentDeliveries(ctx, sc, 100); err == nil {
		c = StatusCheck{Title: "Notifications", State: "ok", Link: "/notifications", LinkText: "Notifications"}
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
			c.State, c.Summary = "bad", plural(len(failing), "channel fails", "channels fail")+" to deliver."
		default:
			c.Summary = fmt.Sprintf("The last %d deliveries went out.", sent)
		}
		add(c)
	}

	// Goliash itself: a newer release, from its own registry.
	c = StatusCheck{Title: "Goliash", State: "ok", Link: "https://github.com/pipozzz/goliash/releases", LinkText: "Releases"}
	switch {
	case !s.updateCheck:
		c.State, c.Summary = "off", "Version "+s.version+"; looking for newer releases is off (GOLIASH_UPDATE_CHECK=off)."
	case s.checker == nil:
		c.Summary = "Version " + s.version + "."
	default:
		look, cancel := context.WithTimeout(ctx, 5*time.Second)
		newer, err := s.checker.NewerGoliash(look, s.version)
		cancel()
		switch {
		case err != nil:
			c.Summary = "Version " + s.version + "; could not look for a newer one: " + err.Error()
		case newer != "":
			c.State, c.Summary = "warn", "Version "+s.version+"; "+newer+" is out."
			c.Link, c.LinkText = "https://github.com/pipozzz/goliash/releases/tag/v"+newer, "Release notes"
			c.Details = append(c.Details, "Upgrade the server first, then the agents (their pages show the command).")
		default:
			c.Summary = "Version " + s.version + ", the newest."
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

func ago(now, t time.Time) string {
	d := now.Sub(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return plural(int(d.Minutes()), "minute", "minutes") + " ago"
	case d < 48*time.Hour:
		return plural(int(d.Hours()), "hour", "hours") + " ago"
	}
	return plural(int(d.Hours()/24), "day", "days") + " ago"
}

func agoOrNever(now, t time.Time) string {
	if t.IsZero() {
		return "never"
	}
	return ago(now, t)
}
