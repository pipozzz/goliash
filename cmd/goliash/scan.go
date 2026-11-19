// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"k8s.io/client-go/tools/clientcmd"

	"github.com/pipozzz/goliash/internal/auth"
	"github.com/pipozzz/goliash/internal/store"
	"github.com/pipozzz/goliash/internal/versions"
)

// scanTarget is one place goliash scan reads: the cluster of a kubeconfig context or a Docker host.
type scanTarget struct {
	name, platform string
	settings       map[string]any
}

// scanTargets finds what to read on this machine: the kubeconfig's context (the one named, or the current one)
// and the Docker host (the one named, DOCKER_HOST, or the local socket). A place switched off is left out.
func scanTargets(kubeContext, dockerHost string, noKube, noDocker bool, getenv func(string) string, exists func(string) bool) []scanTarget {
	var out []scanTarget
	if !noKube {
		if kubeContext == "" {
			if raw, err := clientcmd.NewDefaultClientConfigLoadingRules().Load(); err == nil {
				kubeContext = raw.CurrentContext
			}
		}
		if kubeContext != "" {
			out = append(out, scanTarget{name: scanName(kubeContext), platform: "kubernetes", settings: map[string]any{"kubernetes": map[string]any{"kubeconfig_context": kubeContext}}})
		}
	}
	if !noDocker {
		if dockerHost == "" {
			dockerHost = getenv("DOCKER_HOST")
		}
		if dockerHost == "" {
			for _, sock := range []string{"/var/run/docker.sock", filepath.Join(getenv("HOME"), ".docker/run/docker.sock")} {
				if exists(sock) {
					dockerHost = "unix://" + sock
					break
				}
			}
		}
		if dockerHost != "" {
			out = append(out, scanTarget{name: "docker", platform: "docker", settings: map[string]any{"docker": map[string]any{"docker_host": dockerHost}}})
		}
	}
	return out
}

// scanName makes a target name of a kubeconfig context: an EKS context is an ARN, of which the end is the name.
func scanName(context string) string {
	if i := strings.LastIndexAny(context, "/:"); i >= 0 && i < len(context)-1 {
		context = context[i+1:]
	}
	return context
}

// scanCmd shows what runs here, with nothing installed: a throwaway server that reads the current Kubernetes
// context and the local Docker itself, read-only, and prints what is behind once it knows.
func scanCmd(ctx context.Context, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("scan", flag.ContinueOnError)
	listen := fs.String("listen", "127.0.0.1:8080", "HTTP listen address; local only unless you change it")
	publicURL := fs.String("public-url", "", "URL you open it at (default: http://localhost and the port)")
	kubeContext := fs.String("kube-context", "", "kubeconfig context to read (default: the current one)")
	dockerHost := fs.String("docker-host", "", "Docker API to read (default: DOCKER_HOST or the local socket)")
	noKube := fs.Bool("no-kubernetes", false, "do not read Kubernetes")
	noDocker := fs.Bool("no-docker", false, "do not read Docker")
	env := fs.String("env", "prod", "the environment to show what is found as")
	wait := fs.Duration("wait", 2*time.Minute, "how long to wait for the first results before printing what is known")
	once := fs.Bool("once", false, "print what is behind and stop, without the server to look around in: for CI")
	failOn := fs.String("fail-on", "eol", "with -once, exit with status 3 when something is: eol (past end of life), major (a major behind, or eol), any (behind at all), none")
	format := fs.String("format", "text", "with -once, what to print: text, json, or markdown (for a CI job's summary)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if _, ok := scanFormats[*format]; !ok {
		return fmt.Errorf("-format %q: text, json or markdown", *format)
	}
	if *format != "text" && !*once {
		return errors.New("-format needs -once")
	}
	if _, ok := scanFails[*failOn]; !ok {
		return fmt.Errorf("-fail-on %q: eol, major, any or none", *failOn)
	}
	if *once && !flagSet(fs, "listen") {
		*listen = "127.0.0.1:0" // nobody opens it: any free port, so two scans can run at once
	}
	targets := scanTargets(*kubeContext, *dockerHost, *noKube, *noDocker, os.Getenv, func(p string) bool {
		_, err := os.Stat(p)
		return err == nil
	})
	if len(targets) == 0 {
		return errors.New("nothing to scan: no kubeconfig context and no Docker socket found (name one with -kube-context or -docker-host)")
	}
	if *publicURL == "" {
		*publicURL = localURL(*listen)
	}
	dir, err := os.MkdirTemp("", "goliash-scan-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	defer slog.SetDefault(prev)
	if os.Getenv("GOLIASH_PUSH_SUBJECT") == "" {
		_ = os.Setenv("GOLIASH_PUSH_SUBJECT", "mailto:scan@example.com")
	}
	dsn := filepath.Join(dir, "goliash.db")
	db, ws, err := openDefault(ctx, dsn)
	if err != nil {
		return err
	}
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	link, err := scanSetup(ctx, db, ws, targets, *env, *publicURL, quiet)
	_ = db.Close()
	if err != nil {
		return err
	}
	var names []string
	for _, t := range targets {
		if t.platform == "kubernetes" {
			names = append(names, "the Kubernetes context "+t.name)
		} else {
			names = append(names, "Docker")
		}
	}
	progress := out
	if *format != "text" { // what is printed is for a program: say what happens elsewhere
		progress = os.Stderr
	}
	_, _ = fmt.Fprintf(progress, "\n  Reading %s (read-only; nothing is installed or changed) …\n", strings.Join(names, " and "))
	serveArgs := []string{"-database", dsn, "-listen", *listen, "-public-url", *publicURL, "-log-level", "error", "-upstream-interval", "1m"}
	if *once {
		return scanOnce(ctx, out, dsn, *wait, *failOn, *format, serveArgs)
	}
	go func() {
		rep, _ := scanWait(ctx, dsn, *wait)
		if ctx.Err() != nil {
			return
		}
		_, _ = fmt.Fprint(out, rep.text(15))
		_, _ = fmt.Fprintf(out, "\n  Everything else — vulnerabilities, history, pull requests — is at\n\n  %s\n\n"+
			"  (signs you in; works once, for 15 minutes). Stop with Ctrl-C: nothing is kept.\n"+
			"  To keep watching it: https://goliash.dev/getting-started/\n\n", link)
	}()
	return serve(ctx, serveArgs)
}

// scanOnce runs the throwaway server only until what runs has been compared with its upstream, prints every row,
// and fails as -fail-on says.
func scanOnce(ctx context.Context, out io.Writer, dsn string, wait time.Duration, failOn, format string, serveArgs []string) error {
	sctx, cancel := context.WithCancel(ctx)
	defer cancel()
	served := make(chan error, 1)
	go func() {
		err := serve(sctx, serveArgs)
		cancel() // a server that cannot start ends the wait
		served <- err
	}()
	rep, _ := scanWait(sctx, dsn, wait)
	cancel()
	if err := <-served; err != nil {
		return err
	}
	if ctx.Err() != nil {
		return nil
	}
	rep.FailOn, rep.Failed = failOn, scanFails[failOn](rep.Counts)
	if err := scanFormats[format](out, rep); err != nil {
		return err
	}
	if rep.Failed {
		return exitStatus(3)
	}
	return nil
}

// scanCounts is what a scan found behind: services past end of life, a major release behind, behind at all.
type scanCounts struct {
	EOL    int `json:"end_of_life"`
	Major  int `json:"major_behind"`
	Behind int `json:"behind"`
}

// scanFails says, per -fail-on, whether what was found fails the scan.
var scanFails = map[string]func(scanCounts) bool{
	"eol":   func(c scanCounts) bool { return c.EOL > 0 },
	"major": func(c scanCounts) bool { return c.EOL > 0 || c.Major > 0 },
	"any":   func(c scanCounts) bool { return c.Behind > 0 },
	"none":  func(scanCounts) bool { return false },
}

// exitStatus ends goliash with that status and nothing more to say: the output already said why.
type exitStatus int

func (e exitStatus) Error() string { return fmt.Sprintf("exit status %d", int(e)) }

// flagSet says whether a flag was given on the command line.
func flagSet(fs *flag.FlagSet, name string) bool {
	set := false
	fs.Visit(func(f *flag.Flag) { set = set || f.Name == name })
	return set
}

// scanSetup prepares the throwaway workspace: its targets, collected by the server, images mapped to services by
// themselves, and the person who looks. It returns their sign-in link.
func scanSetup(ctx context.Context, db *store.Store, ws store.Workspace, targets []scanTarget, env, publicURL string, log *slog.Logger) (string, error) {
	if err := db.SetWorkspaceAutoMap(ctx, ws.OrgID, ws.ID, true); err != nil {
		return "", err
	}
	boot := &bootstrapConfig{}
	boot.Environments = append(boot.Environments, struct {
		Name     string `yaml:"name"`
		Position int    `yaml:"position"`
	}{Name: env, Position: 100})
	for _, t := range targets {
		boot.Targets = append(boot.Targets, struct {
			Name        string         `yaml:"name"`
			Environment string         `yaml:"environment"`
			Platform    string         `yaml:"platform"`
			Agent       string         `yaml:"agent"`
			Poll        int            `yaml:"poll_interval_seconds"`
			Settings    map[string]any `yaml:"settings"`
		}{Name: t.name, Environment: env, Platform: t.platform, Settings: t.settings})
	}
	if err := applyBootstrap(ctx, db, ws, boot, publicURL, log); err != nil {
		return "", err
	}
	u, err := db.CreateUser(ctx, ws.OrgID, "you@example.com", "You", store.RoleOwner)
	if err != nil {
		return "", err
	}
	a, err := auth.New(db, log, publicURL, nil)
	if err != nil {
		return "", err
	}
	return a.LoginLink(ctx, u)
}

// scanWait waits until every service found has been compared with its upstream, or for at most the given time,
// and returns what is known; found is false when nothing runs yet.
func scanWait(ctx context.Context, dsn string, wait time.Duration) (scanReport, bool) {
	deadline := time.Now().Add(wait)
	var last scanReport
	found := false
	for {
		select {
		case <-ctx.Done():
			return last, found
		case <-time.After(3 * time.Second):
		}
		if rep, ok := scanSummary(ctx, dsn); ok {
			last, found = rep, true
		}
		if last.Done || time.Now().After(deadline) {
			return last, found
		}
	}
}

// scanSummary reads what is known so far: what runs and what is behind; ok is false while nothing runs.
func scanSummary(ctx context.Context, dsn string) (scanReport, bool) {
	db, err := store.Open(ctx, dsn)
	if err != nil {
		return scanReport{}, false
	}
	defer func() { _ = db.Close() }()
	ws, err := db.EnsureDefaultWorkspace(ctx)
	if err != nil {
		return scanReport{}, false
	}
	o, err := versions.LoadOverview(ctx, db, ws.Scope())
	if err != nil || len(o.Services) == 0 {
		return scanReport{}, false
	}
	return summarize(o), true
}

// scanReport is what a scan found: how many services run, how many were compared with their upstream, and those
// behind it or past end of life, end of life first, then the biggest jump.
type scanReport struct {
	Services int        `json:"services"`
	Checked  int        `json:"compared"`
	Counts   scanCounts `json:"counts"`
	Rows     []scanRow  `json:"behind"`
	Done     bool       `json:"complete"`          // every service was compared
	FailOn   string     `json:"fail_on,omitempty"` // with -once
	Failed   bool       `json:"failed"`
}

// scanRow is one service behind: a release cycle past its end of life, or a jump to the newest release.
type scanRow struct {
	Service string `json:"service"`
	Kind    string `json:"kind"` // end of life, major, minor or patch
	Running string `json:"running"`
	To      string `json:"newest,omitempty"`
	Cycle   string `json:"cycle,omitempty"`
	Ended   string `json:"ended,omitempty"`
}

// detail words a row's versions: "15.6 → 18.7", "1.25.3, cycle 1.25 ended 2024-05-29".
func (r scanRow) detail() string {
	if r.Kind == "end of life" {
		return r.Running + ", cycle " + r.Cycle + " ended " + r.Ended
	}
	return r.Running + " → " + r.To
}

// summarize reads an overview into what a scan reports.
func summarize(o versions.Overview) scanReport {
	rep := scanReport{Services: len(o.Services), Rows: []scanRow{}}
	for id := range o.Services {
		if !o.CheckedAt[id].IsZero() || o.CheckErrs[id] != "" {
			rep.Checked++
		}
	}
	rep.Done = rep.Checked == rep.Services
	rank := map[string]int{"major": 0, "minor": 1, "patch": 2}
	seen := map[scanRow]bool{}
	for _, ds := range o.Drifts {
		for _, d := range ds {
			var det versions.DriftDetail
			_ = json.Unmarshal(d.Detail, &det)
			r := scanRow{Service: o.Services[d.ServiceID].Name, Running: det.Running}
			switch d.Kind {
			case "upstream":
				r.Kind, r.To = string(det.Jump), det.Other
			case "eol":
				r.Kind, r.Cycle, r.Ended = "end of life", det.Other, det.EOL
			default:
				continue
			}
			if !seen[r] {
				seen[r] = true
				rep.Rows = append(rep.Rows, r)
			}
		}
	}
	sort.Slice(rep.Rows, func(i, j int) bool {
		a, b := rep.Rows[i], rep.Rows[j]
		ri, iok := rank[a.Kind]
		rj, jok := rank[b.Kind]
		if iok != jok {
			return !iok // end of life first
		}
		if ri != rj {
			return ri < rj
		}
		return a.Service < b.Service
	})
	behind, eol, major := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, r := range rep.Rows {
		behind[r.Service] = true
		switch r.Kind {
		case "end of life":
			eol[r.Service] = true
		case "major":
			major[r.Service] = true
		}
	}
	rep.Counts = scanCounts{EOL: len(eol), Major: len(major), Behind: len(behind)}
	return rep
}

// headline is the report in one sentence.
func (r scanReport) headline() string {
	if r.Services == 0 {
		return "Nothing found yet: Goliash's page says what each target reports."
	}
	return fmt.Sprintf("%d services run, %d compared with their upstream, %d behind.", r.Services, r.Checked, r.Counts.Behind)
}

// text words the report for the terminal, with at most most rows (0: all).
func (r scanReport) text(most int) string {
	var b strings.Builder
	_, _ = fmt.Fprintf(&b, "\n  %s\n\n", r.headline())
	if len(r.Rows) > 0 {
		tw := tabwriter.NewWriter(&b, 0, 0, 3, ' ', 0)
		_, _ = fmt.Fprintln(tw, "  SERVICE\tBEHIND\tVERSION")
		for i, row := range r.Rows {
			if most > 0 && i == most {
				_, _ = fmt.Fprintf(tw, "  … and %d more\t\t\n", len(r.Rows)-most)
				break
			}
			_, _ = fmt.Fprintf(tw, "  %s\t%s\t%s\n", row.Service, row.Kind, row.detail())
		}
		_ = tw.Flush()
	}
	return b.String()
}

// markdown words the report for a CI job's summary ($GITHUB_STEP_SUMMARY, a merge request note).
func (r scanReport) markdown() string {
	var b strings.Builder
	title := "What runs, and what is behind"
	if r.Failed {
		title += " — failed (" + r.FailOn + ")"
	}
	_, _ = fmt.Fprintf(&b, "### %s\n\n%s\n", title, r.headline())
	if len(r.Rows) > 0 {
		b.WriteString("\n| Service | Behind | Version |\n|---|---|---|\n")
		for _, row := range r.Rows {
			_, _ = fmt.Fprintf(&b, "| %s | %s | %s |\n", mdCell(row.Service), row.Kind, mdCell(row.detail()))
		}
	}
	b.WriteString("\nRead by [goliash scan](https://goliash.dev/reference/cli/#scan-your-own-data-nothing-installed).\n")
	return b.String()
}

// mdCell keeps a value from breaking a Markdown table.
func mdCell(s string) string {
	return strings.NewReplacer("|", "\\|", "\n", " ").Replace(s)
}

// scanFormats print a finished scan.
var scanFormats = map[string]func(io.Writer, scanReport) error{
	"text":     func(w io.Writer, r scanReport) error { _, err := fmt.Fprint(w, r.text(0), "\n"); return err },
	"markdown": func(w io.Writer, r scanReport) error { _, err := fmt.Fprint(w, r.markdown()); return err },
	"json": func(w io.Writer, r scanReport) error {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(r)
	},
}
