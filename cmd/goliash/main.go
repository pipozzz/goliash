// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

// Command goliash is the Goliash server: UI, API and database.
//
//	goliash [serve]                     run the server (default)
//	goliash env create    -name prod -position 30
//	goliash agent create  -name eu-cluster
//	goliash target create -agent eu-cluster -env prod -platform kubernetes -name prod-eu-1
//	goliash matrix | events | drift | check | rule create | service set
//	goliash version
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/pipozzz/goliash/internal/api"
	"github.com/pipozzz/goliash/internal/ingest"
	"github.com/pipozzz/goliash/internal/mapping"
	"github.com/pipozzz/goliash/internal/registry"
	"github.com/pipozzz/goliash/internal/store"
	"github.com/pipozzz/goliash/internal/tokens"
	"github.com/pipozzz/goliash/internal/versions"
	"github.com/pipozzz/goliash/pkg/buildinfo"
)

const usage = `Usage:
  goliash [serve] [flags]                 run the server
  goliash env create -name NAME [-position N]
  goliash agent create -name NAME         prints the agent token once
  goliash target create -agent NAME -env NAME -platform kubernetes|ecs|nomad|swarm -name NAME [-settings JSON] [-poll SECONDS]
  goliash matrix                          service × environment versions
  goliash events [-service NAME] [-limit N]
  goliash drift                           open drifts
  goliash check [-service NAME]           check upstream registries now
  goliash service set -name NAME [-upstream REPO] [-owner O] [-kind own|third_party]
                      [-track patch|minor|major] [-pin-major N] [-tag-filter REGEXP] [-prerelease]
  goliash rule create -match image_repo|workload_name|label|ignore -pattern REGEXP [-service NAME] [-priority N]
  goliash version

Every command takes -database (env GOLIASH_DATABASE_URL, default goliash.db).
`

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, os.Args[1:], os.Stdout); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			os.Exit(2)
		}
		slog.Error("goliash failed", "err", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, out io.Writer) error {
	cmd := "serve"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		cmd, args = args[0], args[1:]
		if len(args) > 0 && args[0] == "create" && (cmd == "env" || cmd == "agent" || cmd == "target" || cmd == "rule") {
			cmd, args = cmd+" create", args[1:]
		}
		if len(args) > 0 && args[0] == "set" && cmd == "service" {
			cmd, args = "service set", args[1:]
		}
	}

	switch cmd {
	case "serve":
		return serve(ctx, args)
	case "env create":
		return envCreate(ctx, args, out)
	case "agent create":
		return agentCreate(ctx, args, out)
	case "target create":
		return targetCreate(ctx, args, out)
	case "matrix":
		return matrixCmd(ctx, args, out)
	case "events":
		return eventsCmd(ctx, args, out)
	case "rule create":
		return ruleCreate(ctx, args, out)
	case "drift":
		return driftCmd(ctx, args, out)
	case "check":
		return checkCmd(ctx, args, out)
	case "service set":
		return serviceSet(ctx, args, out)
	case "version", "-version", "--version":
		_, _ = fmt.Fprintln(out, "goliash", buildinfo.String())
		return nil
	case "help", "-h", "--help":
		_, _ = fmt.Fprint(out, usage)
		return nil
	default:
		_, _ = fmt.Fprint(os.Stderr, usage)
		return fmt.Errorf("unknown command %q", cmd)
	}
}

func newFlags(name string) (*flag.FlagSet, *string) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	dsn := fs.String("database", envOr("GOLIASH_DATABASE_URL", "goliash.db"),
		"SQLite file path or postgres:// URL (env GOLIASH_DATABASE_URL)")
	return fs, dsn
}

// openDefault opens the database and returns the default workspace.
func openDefault(ctx context.Context, dsn string) (*store.Store, store.Workspace, error) {
	db, err := store.Open(ctx, dsn)
	if err != nil {
		return nil, store.Workspace{}, err
	}
	ws, err := db.EnsureDefaultWorkspace(ctx)
	if err != nil {
		_ = db.Close()
		return nil, store.Workspace{}, fmt.Errorf("default workspace: %w", err)
	}
	return db, ws, nil
}

func serve(ctx context.Context, args []string) error {
	fs, dsn := newFlags("serve")
	listen := fs.String("listen", envOr("GOLIASH_LISTEN", ":8080"), "HTTP listen address (env GOLIASH_LISTEN)")
	upstreamEvery := fs.Duration("upstream-interval", time.Hour, "how often public registries are checked for new tags")
	debug := fs.Bool("debug", os.Getenv("GOLIASH_DEBUG") != "", "debug logging (env GOLIASH_DEBUG)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	level := slog.LevelInfo
	if *debug {
		level = slog.LevelDebug
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))

	db, ws, err := openDefault(ctx, *dsn)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	log.Info("database ready", "dialect", db.Dialect(), "workspace", ws.Slug)

	svc := ingest.New(db, log)
	checker := versions.NewChecker(db, registry.New(), log, *upstreamEvery)
	svc.SetUpstreams(checker)
	agents, err := api.NewAgentHandler(db, svc, log)
	if err != nil {
		return err
	}
	mux := http.NewServeMux()
	agents.Register(mux)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		if err := db.Ping(r.Context()); err != nil {
			http.Error(w, "database unavailable", http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte("ok\n"))
	})

	go svc.WatchStale(ctx, time.Minute, nil)
	go svc.RunProcessor(ctx, 10*time.Second)
	go checker.Run(ctx, *upstreamEvery, time.Minute)

	srv := &http.Server{
		Addr:              *listen,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       2 * time.Minute,
		IdleTimeout:       2 * time.Minute,
	}
	errCh := make(chan error, 1)
	go func() {
		log.Info("goliash server starting", "listen", *listen, "version", buildinfo.Version)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
		close(errCh)
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return srv.Shutdown(shutdownCtx) //nolint:contextcheck // the parent context is already cancelled
}

func envCreate(ctx context.Context, args []string, out io.Writer) error {
	fs, dsn := newFlags("env create")
	name := fs.String("name", "", "environment name, e.g. prod")
	position := fs.Int("position", 0, "promotion order; lower comes first (dev 10, staging 20, prod 30)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *name == "" {
		return errors.New("-name is required")
	}
	db, ws, err := openDefault(ctx, *dsn)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	env, err := db.CreateEnvironment(ctx, ws.Scope(), *name, *position)
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintf(out, "environment %s created (%s)\n", env.Name, env.ID)
	return nil
}

func agentCreate(ctx context.Context, args []string, out io.Writer) error {
	fs, dsn := newFlags("agent create")
	name := fs.String("name", "", "agent name, e.g. eu-cluster")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *name == "" {
		return errors.New("-name is required")
	}
	db, ws, err := openDefault(ctx, *dsn)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	token, hash := tokens.New(tokens.Agent)
	a, err := db.CreateAgent(ctx, ws.Scope(), *name, hash)
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintf(out, "agent %s created (%s)\n\nToken (shown once, store it as GOLIASH_AGENT_TOKEN):\n%s\n", a.Name, a.ID, token)
	return nil
}

func targetCreate(ctx context.Context, args []string, out io.Writer) error {
	fs, dsn := newFlags("target create")
	agentName := fs.String("agent", "", "agent that collects the target (empty: the server collects it)")
	envName := fs.String("env", "", "environment the target belongs to")
	platform := fs.String("platform", "", "kubernetes, ecs, nomad or swarm")
	name := fs.String("name", "", "target name, e.g. prod-eu-1")
	settings := fs.String("settings", "", `platform settings as JSON, e.g. {"ecs":{"region":"eu-west-1"}}`)
	poll := fs.Int("poll", 300, "full snapshot interval in seconds")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *envName == "" || *platform == "" || *name == "" {
		return errors.New("-env, -platform and -name are required")
	}
	raw := json.RawMessage(*settings)
	if *settings == "" {
		raw = json.RawMessage(`{}`)
	} else if !json.Valid(raw) {
		return errors.New("-settings is not valid JSON")
	}

	db, ws, err := openDefault(ctx, *dsn)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	env, err := db.GetEnvironmentByName(ctx, ws.Scope(), *envName)
	if err != nil {
		return fmt.Errorf("environment %q: %w", *envName, err)
	}
	t := store.Target{
		Scope: ws.Scope(), EnvironmentID: env.ID, Platform: *platform, Name: *name,
		Settings: raw, PollIntervalSeconds: *poll,
	}
	if *agentName != "" {
		a, err := db.GetAgentByName(ctx, ws.Scope(), *agentName)
		if err != nil {
			return fmt.Errorf("agent %q: %w", *agentName, err)
		}
		t.AgentID = a.ID
	}
	t, err = db.CreateTarget(ctx, t)
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintf(out, "target %s created (%s)\n", t.Name, t.ID)
	return nil
}

func matrixCmd(ctx context.Context, args []string, out io.Writer) error {
	fs, dsn := newFlags("matrix")
	if err := fs.Parse(args); err != nil {
		return err
	}
	db, ws, err := openDefault(ctx, *dsn)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	o, err := versions.LoadOverview(ctx, db, ws.Scope())
	if err != nil {
		return err
	}
	m := o.Matrix

	tw := tabwriter.NewWriter(out, 0, 0, 3, ' ', 0)
	header := []string{"SERVICE"}
	for _, e := range m.Environments {
		header = append(header, strings.ToUpper(e.Name))
	}
	header = append(header, "LATEST")
	_, _ = fmt.Fprintln(tw, strings.Join(header, "\t"))
	for _, row := range m.Rows {
		cols := []string{row.Service.Name}
		for ei, c := range row.Cells {
			if c.Empty() {
				cols = append(cols, "-")
				continue
			}
			var vs []string
			for _, v := range c.Versions {
				vs = append(vs, fmt.Sprintf("%s (%d)", v.Tag, v.Running))
			}
			cell := strings.Join(vs, " + ")
			for _, d := range o.DriftsAt(row.Service.ID, m.Environments[ei].ID) {
				cell += " !" + d.Kind
			}
			cols = append(cols, cell)
		}
		latest := "?"
		if u, ok := o.Upstreams[row.Service.ID]; ok && u.HasLatest {
			latest = u.Latest.Raw
			if u.LatestAny.Raw != u.Latest.Raw {
				latest += " (" + u.LatestAny.Raw + " outside pin)"
			}
		} else if ref := o.Refs[row.Service.ID]; ref.Repo != "" && !versions.IsPublicRegistry(ref.Repo) {
			latest = "? (agent checks " + ref.Repo + ")"
		}
		cols = append(cols, latest)
		_, _ = fmt.Fprintln(tw, strings.Join(cols, "\t"))
	}
	_ = tw.Flush()
	if m.Unmapped > 0 {
		_, _ = fmt.Fprintf(out, "\n%d workload(s) not mapped to a service yet.\n", m.Unmapped)
	}
	return nil
}

func driftCmd(ctx context.Context, args []string, out io.Writer) error {
	fs, dsn := newFlags("drift")
	if err := fs.Parse(args); err != nil {
		return err
	}
	db, ws, err := openDefault(ctx, *dsn)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	o, err := versions.LoadOverview(ctx, db, ws.Scope())
	if err != nil {
		return err
	}
	tw := tabwriter.NewWriter(out, 0, 0, 3, ' ', 0)
	_, _ = fmt.Fprintln(tw, "SERVICE\tENV\tKIND\tSINCE\tDETAIL")
	for _, ds := range o.Drifts {
		for _, d := range ds {
			var det versions.DriftDetail
			_ = json.Unmarshal(d.Detail, &det)
			detail := det.Running + " behind " + det.Other
			switch d.Kind {
			case "env":
				detail = fmt.Sprintf("%s, %s runs %s", det.Running, det.OtherIn, det.Other)
			case "upstream":
				detail = fmt.Sprintf("%s, upstream %s (%s)", det.Running, det.Other, det.Jump)
			case "inconsistent":
				var parts []string
				for t, v := range det.Targets {
					parts = append(parts, t+"="+v)
				}
				sort.Strings(parts)
				detail = strings.Join(parts, " ")
			}
			_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", o.Services[d.ServiceID].Name, o.Envs[d.EnvironmentID].Name,
				d.Kind, time.Since(d.Since).Round(time.Minute), detail)
		}
	}
	return tw.Flush()
}

func checkCmd(ctx context.Context, args []string, out io.Writer) error {
	fs, dsn := newFlags("check")
	service := fs.String("service", "", "check only this service")
	if err := fs.Parse(args); err != nil {
		return err
	}
	db, ws, err := openDefault(ctx, *dsn)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	checker := versions.NewChecker(db, registry.New(), log, time.Minute)
	sc := ws.Scope()
	if *service != "" {
		svc, err := db.GetServiceByName(ctx, sc, *service)
		if err != nil {
			return fmt.Errorf("service %q: %w", *service, err)
		}
		if err := checker.CheckService(ctx, sc, svc.ID); err != nil {
			return err
		}
	} else {
		if err := checker.CheckUpstreams(ctx, sc); err != nil {
			return err
		}
		if err := checker.EvaluateDrift(ctx, sc); err != nil {
			return err
		}
	}
	_, _ = fmt.Fprintln(out, "upstreams checked")
	return matrixCmd(ctx, []string{"-database", *dsn}, out)
}

func serviceSet(ctx context.Context, args []string, out io.Writer) error {
	fs, dsn := newFlags("service set")
	name := fs.String("name", "", "service name")
	upstream := fs.String("upstream", "", "image repository to read releases from, e.g. docker.io/library/postgres")
	owner := fs.String("owner", "", "owning team or person")
	kind := fs.String("kind", "", "own or third_party")
	track := fs.String("track", "", "smallest version jump that alerts: patch, minor or major")
	pinMajor := fs.Int("pin-major", -1, "stay on this major; newer majors are information only (-1: no pin)")
	tagFilter := fs.String("tag-filter", "", "regular expression upstream tags must match")
	prerelease := fs.Bool("prerelease", false, "consider alpha, beta and rc tags")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *name == "" {
		return errors.New("-name is required")
	}
	db, ws, err := openDefault(ctx, *dsn)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	svc, err := db.EnsureService(ctx, ws.Scope(), *name)
	if err != nil {
		return err
	}
	policy, err := versions.ParsePolicy(svc.VersionPolicy)
	if err != nil {
		return err
	}
	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })
	if set["upstream"] {
		svc.Upstream = *upstream
	}
	if set["owner"] {
		svc.Owner = *owner
	}
	if set["kind"] {
		svc.Kind = *kind
	}
	if set["track"] {
		policy.Track = versions.Jump(*track)
	}
	if set["pin-major"] {
		policy.PinMajor = nil
		if *pinMajor >= 0 {
			policy.PinMajor = pinMajor
		}
	}
	if set["tag-filter"] {
		policy.TagFilter = *tagFilter
	}
	if set["prerelease"] {
		policy.Prerelease = *prerelease
	}
	raw, err := json.Marshal(policy)
	if err != nil {
		return err
	}
	if _, err := versions.ParsePolicy(raw); err != nil {
		return err
	}
	svc.VersionPolicy = raw
	if err := db.UpdateService(ctx, svc); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(out, "service %s updated: upstream=%q policy=%s\n", svc.Name, svc.Upstream, raw)
	return nil
}

func eventsCmd(ctx context.Context, args []string, out io.Writer) error {
	fs, dsn := newFlags("events")
	service := fs.String("service", "", "only events of this service")
	limit := fs.Int("limit", 50, "number of events")
	if err := fs.Parse(args); err != nil {
		return err
	}
	db, ws, err := openDefault(ctx, *dsn)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	sc := ws.Scope()
	f := store.EventFilter{Limit: *limit}
	if *service != "" {
		svc, err := db.GetServiceByName(ctx, sc, *service)
		if err != nil {
			return fmt.Errorf("service %q: %w", *service, err)
		}
		f.ServiceID = svc.ID
	}
	evs, err := db.ListEvents(ctx, sc, f)
	if err != nil {
		return err
	}
	names := map[string]string{}
	services, _ := db.ListServices(ctx, sc)
	for _, s := range services {
		names[s.ID] = s.Name
	}
	envs, _ := db.ListEnvironments(ctx, sc)
	for _, e := range envs {
		names[e.ID] = e.Name
	}
	targets, _ := db.ListTargets(ctx, sc)
	for _, t := range targets {
		names[t.ID] = t.Name
	}
	tw := tabwriter.NewWriter(out, 0, 0, 3, ' ', 0)
	_, _ = fmt.Fprintln(tw, "TIME (UTC)\tEVENT\tSERVICE\tENV\tTARGET\tCHANGE")
	for _, e := range evs {
		svc := names[e.ServiceID]
		if svc == "" {
			svc = "(unmapped)"
		}
		change := strings.TrimSpace(e.FromVersion + " → " + e.ToVersion)
		switch e.Type {
		case "deployed":
			change = e.ToVersion
		case "removed":
			change = e.FromVersion
		}
		if e.Note != "" {
			change += " (" + e.Note + ")"
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", e.At.Format("2006-01-02 15:04"), e.Type, svc,
			names[e.EnvironmentID], names[e.TargetID], change)
	}
	return tw.Flush()
}

func ruleCreate(ctx context.Context, args []string, out io.Writer) error {
	fs, dsn := newFlags("rule create")
	match := fs.String("match", "", "image_repo, workload_name, label (pattern key=regexp) or ignore")
	pattern := fs.String("pattern", "", "regular expression, matched against the whole value")
	service := fs.String("service", "", "service the rule maps to (created if missing); not for ignore")
	priority := fs.Int("priority", 100, "lower runs first")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *match == "" || *pattern == "" || (*match != "ignore" && *service == "") {
		return errors.New("-match and -pattern are required, and -service unless -match ignore")
	}
	db, ws, err := openDefault(ctx, *dsn)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	r := store.MappingRule{Scope: ws.Scope(), Priority: *priority, MatchType: *match, Pattern: *pattern}
	if _, errs := mapping.New([]store.MappingRule{r}); len(errs) > 0 {
		return errs[0]
	}
	if *service != "" {
		svc, err := db.EnsureService(ctx, ws.Scope(), *service)
		if err != nil {
			return err
		}
		r.ServiceID = svc.ID
	}
	r, err = db.CreateMappingRule(ctx, r)
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintf(out, "rule %s created; it applies from the next snapshot\n", r.ID)
	return nil
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
