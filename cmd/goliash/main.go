// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

// Command goliash is the Goliash server: UI, API and database.
//
//	goliash [serve]                     run the server (default)
//	goliash env create    -name prod -position 30
//	goliash agent create  -name eu-cluster
//	goliash target create -agent eu-cluster -env prod -platform kubernetes -name prod-eu-1
//	goliash matrix | events | rule create
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
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/pipozzz/goliash/internal/api"
	"github.com/pipozzz/goliash/internal/ingest"
	"github.com/pipozzz/goliash/internal/mapping"
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
	sc := ws.Scope()
	services, err := db.ListServices(ctx, sc)
	if err != nil {
		return err
	}
	envs, err := db.ListEnvironments(ctx, sc)
	if err != nil {
		return err
	}
	targets, err := db.ListTargets(ctx, sc)
	if err != nil {
		return err
	}
	active, err := db.ListActiveInstances(ctx, sc)
	if err != nil {
		return err
	}
	m := versions.BuildMatrix(services, envs, targets, active)

	tw := tabwriter.NewWriter(out, 0, 0, 3, ' ', 0)
	header := []string{"SERVICE"}
	for _, e := range m.Environments {
		header = append(header, strings.ToUpper(e.Name))
	}
	_, _ = fmt.Fprintln(tw, strings.Join(header, "\t"))
	for _, row := range m.Rows {
		cols := []string{row.Service.Name}
		for _, c := range row.Cells {
			if c.Empty() {
				cols = append(cols, "-")
				continue
			}
			var vs []string
			for _, v := range c.Versions {
				vs = append(vs, fmt.Sprintf("%s (%d)", v.Tag, v.Running))
			}
			cols = append(cols, strings.Join(vs, " + "))
		}
		_, _ = fmt.Fprintln(tw, strings.Join(cols, "\t"))
	}
	_ = tw.Flush()
	if m.Unmapped > 0 {
		_, _ = fmt.Fprintf(out, "\n%d workload(s) not mapped to a service yet.\n", m.Unmapped)
	}
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
