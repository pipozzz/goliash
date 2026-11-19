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
	if err := fs.Parse(args); err != nil {
		return err
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
	_, _ = fmt.Fprintf(out, "\n  Reading %s (read-only; nothing is installed or changed) …\n", strings.Join(names, " and "))
	go func() {
		sum := scanWait(ctx, dsn, *wait)
		if ctx.Err() != nil {
			return
		}
		_, _ = fmt.Fprint(out, sum)
		_, _ = fmt.Fprintf(out, "\n  Everything else — vulnerabilities, history, pull requests — is at\n\n  %s\n\n"+
			"  (signs you in; works once, for 15 minutes). Stop with Ctrl-C: nothing is kept.\n"+
			"  To keep watching it: https://goliash.dev/getting-started/\n\n", link)
	}()
	return serve(ctx, []string{"-database", dsn, "-listen", *listen, "-public-url", *publicURL, "-log-level", "error", "-upstream-interval", "1m"})
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
// and returns the summary to print.
func scanWait(ctx context.Context, dsn string, wait time.Duration) string {
	deadline := time.Now().Add(wait)
	var last string
	for {
		select {
		case <-ctx.Done():
			return last
		case <-time.After(3 * time.Second):
		}
		sum, done := scanSummary(ctx, dsn)
		if sum != "" {
			last = sum
		}
		if done || time.Now().After(deadline) {
			if last == "" {
				return "\n  Nothing found yet: the page below says what each target reports.\n"
			}
			return last
		}
	}
}

// scanSummary reads what is known so far: what runs and what is behind. done says every service was compared
// with its upstream.
func scanSummary(ctx context.Context, dsn string) (string, bool) {
	db, err := store.Open(ctx, dsn)
	if err != nil {
		return "", false
	}
	defer func() { _ = db.Close() }()
	ws, err := db.EnsureDefaultWorkspace(ctx)
	if err != nil {
		return "", false
	}
	o, err := versions.LoadOverview(ctx, db, ws.Scope())
	if err != nil || len(o.Services) == 0 {
		return "", false
	}
	return summarize(o)
}

// summarize words an overview for the terminal: how many services run, and those behind their upstream or past
// end of life, the biggest jump first.
func summarize(o versions.Overview) (string, bool) {
	checked := 0
	for id := range o.Services {
		if !o.CheckedAt[id].IsZero() || o.CheckErrs[id] != "" {
			checked++
		}
	}
	type row struct{ service, kind, detail string }
	var rows []row
	rank := map[string]int{"major": 0, "minor": 1, "patch": 2}
	seen := map[string]bool{}
	for _, ds := range o.Drifts {
		for _, d := range ds {
			var det versions.DriftDetail
			_ = json.Unmarshal(d.Detail, &det)
			name := o.Services[d.ServiceID].Name
			var r row
			switch d.Kind {
			case "upstream":
				r = row{name, string(det.Jump), det.Running + " → " + det.Other}
			case "eol":
				r = row{name, "end of life", det.Running + ", cycle " + det.Other + " ended " + det.EOL}
			default:
				continue
			}
			if key := r.service + "|" + r.kind + "|" + r.detail; !seen[key] {
				seen[key] = true
				rows = append(rows, r)
			}
		}
	}
	sort.Slice(rows, func(i, j int) bool {
		ri, iok := rank[rows[i].kind]
		rj, jok := rank[rows[j].kind]
		if iok != jok {
			return !iok // end of life first
		}
		if ri != rj {
			return ri < rj
		}
		return rows[i].service < rows[j].service
	})
	var b strings.Builder
	behind := map[string]bool{}
	for _, r := range rows {
		behind[r.service] = true
	}
	_, _ = fmt.Fprintf(&b, "\n  %d services run, %d compared with their upstream, %d behind.\n\n", len(o.Services), checked, len(behind))
	if len(rows) > 0 {
		tw := tabwriter.NewWriter(&b, 0, 0, 3, ' ', 0)
		_, _ = fmt.Fprintln(tw, "  SERVICE\tBEHIND\tVERSION")
		const most = 15
		for i, r := range rows {
			if i == most {
				_, _ = fmt.Fprintf(tw, "  … and %d more\t\t\n", len(rows)-most)
				break
			}
			_, _ = fmt.Fprintf(tw, "  %s\t%s\t%s\n", r.service, r.kind, r.detail)
		}
		_ = tw.Flush()
	}
	return b.String(), checked == len(o.Services)
}
