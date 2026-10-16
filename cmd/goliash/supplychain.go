// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/pipozzz/goliash/internal/versions"
)

// errFound is the error licenses returns when -fail-on matched, for a non-zero exit in scripts.
var errFound = errors.New("packages under the licenses named by -fail-on run")

// licensesCmd lists the licenses of the packages in running images, those that need a look first; -fail-on
// makes it exit non-zero when a package of the named classes runs, for a check in CI or cron.
func licensesCmd(ctx context.Context, args []string, out io.Writer) error {
	fs, dsn := newFlags("licenses")
	class := fs.String("class", "attention", "attention (copyleft that reaches your code, source-available, unknown), all, or one class")
	env := fs.String("environment", "", "only this environment")
	withOS := fs.Bool("os", false, "operating system packages (deb, apk, rpm) too")
	failOn := fs.String("fail-on", "", "exit 1 when an application package of these classes runs, e.g. network-copyleft,source-available")
	if err := fs.Parse(args); err != nil {
		return err
	}
	db, ws, err := openDefault(ctx, *dsn)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	envID := ""
	if *env != "" {
		e, err := db.GetEnvironmentByName(ctx, ws.Scope(), *env)
		if err != nil {
			return fmt.Errorf("environment %q: %w", *env, err)
		}
		envID = e.ID
	}
	rep, err := versions.LoadLicenseReport(ctx, db, ws.Scope(), envID)
	if err != nil {
		return err
	}
	fail := map[string]bool{}
	for _, c := range splitList(*failOn) {
		fail[c] = true
	}
	tw := tabwriter.NewWriter(out, 0, 0, 3, ' ', 0)
	_, _ = fmt.Fprintln(tw, "CLASS\tPACKAGE\tVERSION\tLICENSE\tRUNNING IN")
	failed := false
	for _, p := range rep.Packages {
		if !p.OS && fail[string(p.Class)] {
			failed = true
		}
		switch {
		case p.OS && !*withOS,
			*class == "attention" && !p.Class.Attention(),
			*class != "attention" && *class != "all" && string(p.Class) != *class:
			continue
		}
		var where []string
		for _, u := range p.Where {
			where = append(where, u.Service+" ("+u.Environment+")")
		}
		lic := p.License
		if lic == "" {
			lic = "-"
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", p.Class, p.Name, p.Version, lic, strings.Join(where, ", "))
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	if rep.LicensesUnread > 0 {
		_, _ = fmt.Fprintf(out, "\n%d images' SBOMs were read before licenses were kept; they are read again soon.\n", rep.LicensesUnread)
	}
	if failed {
		return errFound
	}
	return nil
}

// imagesCmd shows what a service's running images are made of: size against its budget, base, signature,
// SBOM and provenance per environment, and what the last deploy changed in the layers.
func imagesCmd(ctx context.Context, args []string, out io.Writer) error {
	fs, dsn := newFlags("images")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("usage: goliash images SERVICE")
	}
	db, ws, err := openDefault(ctx, *dsn)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	sc := ws.Scope()
	svc, err := db.GetServiceByName(ctx, sc, fs.Arg(0))
	if err != nil {
		return fmt.Errorf("service %q: %w", fs.Arg(0), err)
	}
	envs, err := db.ListEnvironments(ctx, sc)
	if err != nil {
		return err
	}
	active, err := db.ListActiveInstances(ctx, sc)
	if err != nil {
		return err
	}
	evidence, err := db.ListImageEvidence(ctx, sc)
	if err != nil {
		return err
	}
	budget := versions.SizeBudget(svc)
	tw := tabwriter.NewWriter(out, 0, 0, 3, ' ', 0)
	_, _ = fmt.Fprintln(tw, "ENVIRONMENT\tIMAGE\tSIZE\tBASE\tSIGNED\tSBOM\tPROVENANCE")
	seen := map[string]bool{}
	for _, e := range envs {
		for _, in := range active {
			if in.ServiceID != svc.ID || in.EnvironmentID != e.ID || !in.IsMain || seen[e.ID+in.Image+in.Digest] {
				continue
			}
			seen[e.ID+in.Image+in.Digest] = true
			ev, ok := evidence[versions.ParseImage(in.Image).Repo()+"@"+in.Digest]
			size, base, signed, sbom, prov := "-", "-", "-", "-", "-"
			if ok && ev.Size > 0 {
				size = megabytes(ev.Size)
				if budget > 0 && ev.Size > budget {
					size += " (over " + megabytes(budget) + ")"
				}
			}
			if ok && ev.BaseStatus != "" {
				base = ev.BaseStatus
				if ev.BaseStatus == "outdated" && !ev.BaseSince.IsZero() {
					base += fmt.Sprintf(", %d days", int(time.Since(ev.BaseSince).Hours()/24))
				}
			}
			if ok && !ev.CheckedAt.IsZero() && ev.Error == "" {
				signed, sbom, prov = yesNo(ev.Signed), yesNo(ev.SBOM), yesNo(ev.Provenance)
			}
			_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", e.Name, in.Image, size, base, signed, sbom, prov)
		}
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	periods, err := db.ServiceImagePeriods(ctx, sc, svc.ID, time.Now().AddDate(0, 0, -90))
	if err != nil {
		return err
	}
	if c := versions.LastSizeChange(periods, evidence, envs); c != nil {
		_, _ = fmt.Fprintf(out, "\nLast change in %s: %s → %s, %s → %s\n", c.Env, c.From, c.To, megabytes(c.Before), megabytes(c.After))
		for _, l := range c.Added {
			_, _ = fmt.Fprintf(out, "  + %-8s %s\n", megabytes(l.Size), orDash(l.Command))
		}
		for _, l := range c.Removed {
			_, _ = fmt.Fprintf(out, "  - %-8s %s\n", megabytes(l.Size), orDash(l.Command))
		}
	}
	return nil
}

func megabytes(n int64) string { return fmt.Sprintf("%d MB", (n+500_000)/1_000_000) }

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
