// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

// Command goliash is the Goliash server: UI, API and database.
//
//	goliash [serve]                     run the server (default)
//	goliash env create    -name prod -position 30
//	goliash agent create  -name eu-cluster
//	goliash target create -agent eu-cluster -env prod -platform kubernetes -name prod-eu-1
//	goliash matrix | events | drift | check | rule create | service set
//	goliash channel create | channel test | notify create | ack
//	goliash user create | login-link | token create
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
	"github.com/pipozzz/goliash/internal/auth"
	"github.com/pipozzz/goliash/internal/demo"
	"github.com/pipozzz/goliash/internal/ingest"
	"github.com/pipozzz/goliash/internal/mapping"
	"github.com/pipozzz/goliash/internal/notifier"
	"github.com/pipozzz/goliash/internal/registry"
	"github.com/pipozzz/goliash/internal/store"
	"github.com/pipozzz/goliash/internal/tokens"
	"github.com/pipozzz/goliash/internal/ui"
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
  goliash channel create -type slack|webhook|email -name NAME [-url URL] [-secret S] [-to a@b,c@d]
  goliash channel test -name NAME
  goliash notify create -channel NAME [-events new_release,drift_detected] [-mode instant|daily|weekly]
                        [-services a,b] [-owners x] [-envs prod] [-min-jump minor] [-digest-hour 8]
  goliash ack -service NAME -kind release|drift [-until-version 2.1.0] [-for 336h] [-env prod]
  goliash user create -email E [-role owner|admin|member|viewer] [-name N]
  goliash login-link -email E             one-time sign-in link (creates the first user as owner)
  goliash token create -name N            API token for /api/v1 and /metrics (shown once)
  goliash rule create -match image_repo|workload_name|label|ignore -pattern REGEXP [-service NAME] [-priority N]
  goliash healthcheck                     exit 0 when the local server answers /healthz (container health checks)
  goliash demo                            fill the workspace with three weeks of example data
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
		if len(args) > 0 && cmd == "channel" && (args[0] == "create" || args[0] == "test") {
			cmd, args = "channel "+args[0], args[1:]
		}
		if len(args) > 0 && cmd == "notify" && args[0] == "create" {
			cmd, args = "notify create", args[1:]
		}
		if len(args) > 0 && (cmd == "user" || cmd == "token") && args[0] == "create" {
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
	case "drift":
		return driftCmd(ctx, args, out)
	case "check":
		return checkCmd(ctx, args, out)
	case "service set":
		return serviceSet(ctx, args, out)
	case "channel create":
		return channelCreate(ctx, args, out)
	case "channel test":
		return channelTest(ctx, args, out)
	case "notify create":
		return notifyCreate(ctx, args, out)
	case "ack":
		return ackCmd(ctx, args, out)
	case "user create":
		return userCreate(ctx, args, out)
	case "login-link":
		return loginLink(ctx, args, out)
	case "token create":
		return tokenCreate(ctx, args, out)
	case "demo":
		return demoCmd(ctx, args, out)
	case "healthcheck":
		return healthcheck(ctx)
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
	keepSnapshots := fs.Int("keep-snapshots", 20, "processed snapshots kept per target; older ones are deleted hourly")
	publicURL := fs.String("public-url", envOr("GOLIASH_PUBLIC_URL", "http://localhost:8080"),
		"URL people use to reach this server, for sign-in links and cookies (env GOLIASH_PUBLIC_URL)")
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
	notify := notifier.New(db, log, notifier.DefaultSenders(&http.Client{Timeout: 30 * time.Second}, smtpFromEnv()))
	hub := ui.NewHub()
	svc.OnEvents(func(sc store.Scope, evs []store.Event) {
		hub.Publish(sc.WorkspaceID)
		notify.Handle(sc, evs)
		// A deploy can open or close drift; do not wait for the next minute.
		if err := checker.EvaluateDrift(ctx, sc); err != nil && ctx.Err() == nil {
			log.Error("drift evaluation failed", "err", err)
		}
	})
	checker.OnEvents(func(sc store.Scope, evs []store.Event) {
		hub.Publish(sc.WorkspaceID)
		notify.Handle(sc, evs)
	})
	agents, err := api.NewAgentHandler(db, svc, log)
	if err != nil {
		return err
	}
	authn, err := newAuth(ctx, db, log, *publicURL)
	if err != nil {
		return err
	}
	mux := http.NewServeMux()
	agents.Register(mux)
	authn.Routes(mux)
	api.NewPublicHandler(db, authn, log).Register(mux)
	ui.New(ui.Options{
		Store: db, Auth: authn, Checker: checker, Notifier: notify, Hub: hub, Log: log, PublicURL: *publicURL,
		SMTP: smtpFromEnv().Addr != "",
	}).Register(mux)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		if err := db.Ping(r.Context()); err != nil {
			http.Error(w, "database unavailable", http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte("ok\n"))
	})

	go svc.WatchStale(ctx, time.Minute, notify.AgentStale)
	go notify.Run(ctx, 15*time.Second)
	go svc.RunProcessor(ctx, 10*time.Second)
	go checker.Run(ctx, *upstreamEvery, time.Minute)
	go housekeeping(ctx, db, log, *keepSnapshots)

	srv := &http.Server{
		Addr: *listen,
		// Browsers may not send state-changing requests from other origins (CSRF);
		// agents and API clients send no Origin and are unaffected.
		Handler:           http.NewCrossOriginProtection().Handler(mux),
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
	cliAudit(ctx, db, ws, "environment.create", "environment", env.Name)
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
	cliAudit(ctx, db, ws, "agent.create", "agent", a.Name)
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
	cliAudit(ctx, db, ws, "target.create", "target", t.Name, "platform", t.Platform)
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
	// Notifications are queued here and delivered by the running server.
	checker.OnEvents(notifier.New(db, log, nil).Handle)
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
	cliAudit(ctx, db, ws, "service.update", "service", svc.Name, "policy", string(raw))
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
	cliAudit(ctx, db, ws, "mapping.create", "match", r.MatchType, "pattern", r.Pattern)
	_, _ = fmt.Fprintf(out, "rule %s created; it applies from the next snapshot\n", r.ID)
	return nil
}

// newAuth configures sign-in from the environment: e-mailed magic links when SMTP is
// set, OIDC when GOLIASH_OIDC_ISSUER is set.
func newAuth(ctx context.Context, db *store.Store, log *slog.Logger, publicURL string) (*auth.Auth, error) {
	var mail auth.MailFunc
	if cfg := smtpFromEnv(); cfg.Addr != "" && cfg.From != "" {
		mail = func(ctx context.Context, to, subject, body string) error {
			ch := store.Channel{Name: "sign-in", Config: json.RawMessage(fmt.Sprintf(`{"to":[%q]}`, to))}
			return notifier.Email{Config: cfg}.SendPlain(ctx, ch, subject, body)
		}
	}
	a, err := auth.New(db, log, publicURL, mail)
	if err != nil {
		return nil, err
	}
	if issuer := os.Getenv("GOLIASH_OIDC_ISSUER"); issuer != "" {
		o, err := auth.NewOIDC(ctx, auth.OIDCConfig{
			Issuer: issuer, ClientID: os.Getenv("GOLIASH_OIDC_CLIENT_ID"), ClientSecret: os.Getenv("GOLIASH_OIDC_CLIENT_SECRET"),
			Name: os.Getenv("GOLIASH_OIDC_NAME"), Domains: splitList(os.Getenv("GOLIASH_OIDC_DOMAINS")),
		}, publicURL)
		if err != nil {
			return nil, err
		}
		a.SetOIDC(o)
		log.Info("oidc sign-in enabled", "issuer", issuer)
	}
	return a, nil
}

func userCreate(ctx context.Context, args []string, out io.Writer) error {
	fs, dsn := newFlags("user create")
	email := fs.String("email", "", "e-mail address")
	role := fs.String("role", store.RoleViewer, "owner, admin, member or viewer")
	name := fs.String("name", "", "display name")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *email == "" || store.RoleRank(*role) == 0 {
		return errors.New("-email and a valid -role are required")
	}
	db, ws, err := openDefault(ctx, *dsn)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	u, err := db.CreateUser(ctx, ws.OrgID, *email, *name, *role)
	if err != nil {
		return err
	}
	cliAudit(ctx, db, ws, "user.create", "user", u.Email, "role", u.Role)
	_, _ = fmt.Fprintf(out, "user %s created with role %s\n", u.Email, u.Role)
	return nil
}

func loginLink(ctx context.Context, args []string, out io.Writer) error {
	fs, dsn := newFlags("login-link")
	email := fs.String("email", "", "e-mail address of the user")
	publicURL := fs.String("public-url", envOr("GOLIASH_PUBLIC_URL", "http://localhost:8080"), "server URL (env GOLIASH_PUBLIC_URL)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *email == "" {
		return errors.New("-email is required")
	}
	db, ws, err := openDefault(ctx, *dsn)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	u, err := db.GetUserByEmail(ctx, ws.OrgID, *email)
	if errors.Is(err, store.ErrNotFound) {
		n, cerr := db.CountUsers(ctx, ws.OrgID)
		if cerr != nil {
			return cerr
		}
		if n > 0 {
			return fmt.Errorf("no user %s; create one with goliash user create", *email)
		}
		if u, err = db.CreateUser(ctx, ws.OrgID, *email, "", store.RoleOwner); err == nil {
			_, _ = fmt.Fprintf(out, "first user %s created as owner\n", u.Email)
		}
	}
	if err != nil {
		return err
	}
	a, err := auth.New(db, slog.New(slog.NewTextHandler(io.Discard, nil)), *publicURL, nil)
	if err != nil {
		return err
	}
	link, err := a.LoginLink(ctx, u)
	if err != nil {
		return err
	}
	cliAudit(ctx, db, ws, "user.login_link", "user", u.Email)
	_, _ = fmt.Fprintf(out, "Sign-in link for %s (works once, expires in 15 minutes):\n%s\n", u.Email, link)
	return nil
}

func tokenCreate(ctx context.Context, args []string, out io.Writer) error {
	fs, dsn := newFlags("token create")
	name := fs.String("name", "", "what the token is for, e.g. prometheus")
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
	token, hash := tokens.New(tokens.API)
	if _, err := db.CreateAPIToken(ctx, ws.Scope(), *name, hash); err != nil {
		return err
	}
	cliAudit(ctx, db, ws, "api_token.create", "token", *name)
	_, _ = fmt.Fprintf(out, "API token %s (shown once):\n%s\n", *name, token)
	return nil
}

// housekeeping deletes data nothing reads any more, hourly.
func housekeeping(ctx context.Context, db *store.Store, log *slog.Logger, keep int) {
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		r, err := db.Housekeep(ctx, keep)
		switch {
		case err != nil && ctx.Err() == nil:
			log.Error("housekeeping failed", "err", err)
		case r != (store.HousekeepingResult{}):
			log.Info("housekeeping", "snapshots", r.Snapshots, "notifications", r.Notifications,
				"sessions", r.Sessions, "login_tokens", r.LoginTokens)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// healthcheck calls /healthz on the local listen port; distroless images have no curl.
func healthcheck(ctx context.Context) error {
	listen := envOr("GOLIASH_LISTEN", ":8080")
	host, port, ok := strings.Cut(listen, ":")
	if !ok {
		return fmt.Errorf("cannot read port from GOLIASH_LISTEN=%q", listen)
	}
	if host == "" || host == "0.0.0.0" {
		host = "127.0.0.1"
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+host+":"+port+"/healthz", nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("/healthz answered %d", resp.StatusCode)
	}
	return nil
}

func demoCmd(ctx context.Context, args []string, out io.Writer) error {
	fs, dsn := newFlags("demo")
	if err := fs.Parse(args); err != nil {
		return err
	}
	db, _, err := openDefault(ctx, *dsn)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	if err := demo.Seed(ctx, db, slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
		return err
	}
	_, _ = fmt.Fprintln(out, "Demo data added: 4 demo-* targets in dev, staging and prod, 7 services, three weeks of history.")
	return nil
}

// cliAudit records a change made from the command line.
func cliAudit(ctx context.Context, db *store.Store, ws store.Workspace, action string, kv ...string) {
	details := map[string]string{}
	for i := 0; i+1 < len(kv); i += 2 {
		details[kv[i]] = kv[i+1]
	}
	if err := db.Audit(ctx, store.AuditEntry{OrgID: ws.OrgID, WorkspaceID: ws.ID, Actor: "cli", Action: action, Details: details}); err != nil {
		slog.Warn("audit log write failed", "err", err)
	}
}

func smtpFromEnv() notifier.SMTPConfig {
	return notifier.SMTPConfig{
		Addr:     os.Getenv("GOLIASH_SMTP_ADDR"),
		Username: os.Getenv("GOLIASH_SMTP_USERNAME"),
		Password: os.Getenv("GOLIASH_SMTP_PASSWORD"),
		From:     os.Getenv("GOLIASH_SMTP_FROM"),
	}
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func channelCreate(ctx context.Context, args []string, out io.Writer) error {
	fs, dsn := newFlags("channel create")
	typ := fs.String("type", "", "slack, webhook or email")
	name := fs.String("name", "", "channel name")
	url := fs.String("url", "", "slack incoming webhook or webhook URL")
	secret := fs.String("secret", "", "webhook signing secret (HMAC-SHA256)")
	to := fs.String("to", "", "comma-separated e-mail recipients")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg := map[string]any{}
	switch *typ {
	case "slack", "webhook":
		if *url == "" {
			return errors.New("-url is required")
		}
		cfg["url"] = *url
		if *secret != "" {
			cfg["secret"] = *secret
		}
	case "email":
		if *to == "" {
			return errors.New("-to is required")
		}
		cfg["to"] = splitList(*to)
	default:
		return errors.New("-type must be slack, webhook or email")
	}
	if *name == "" {
		return errors.New("-name is required")
	}
	db, ws, err := openDefault(ctx, *dsn)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	raw, _ := json.Marshal(cfg)
	ch, err := db.CreateChannel(ctx, store.Channel{Scope: ws.Scope(), Type: *typ, Name: *name, Config: raw})
	if err != nil {
		return err
	}
	cliAudit(ctx, db, ws, "channel.create", "channel", ch.Name, "type", ch.Type)
	_, _ = fmt.Fprintf(out, "channel %s created (%s)\n", ch.Name, ch.ID)
	return nil
}

func channelTest(ctx context.Context, args []string, out io.Writer) error {
	fs, dsn := newFlags("channel test")
	name := fs.String("name", "", "channel name")
	if err := fs.Parse(args); err != nil {
		return err
	}
	db, ws, err := openDefault(ctx, *dsn)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	ch, err := db.GetChannelByName(ctx, ws.Scope(), *name)
	if err != nil {
		return fmt.Errorf("channel %q: %w", *name, err)
	}
	n := notifier.New(db, slog.New(slog.NewTextHandler(io.Discard, nil)),
		notifier.DefaultSenders(&http.Client{Timeout: 30 * time.Second}, smtpFromEnv()))
	if err := n.SendTest(ctx, ch, ws.Name); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(out, "test notification sent to %s\n", ch.Name)
	return nil
}

func notifyCreate(ctx context.Context, args []string, out io.Writer) error {
	fs, dsn := newFlags("notify create")
	channel := fs.String("channel", "", "channel name")
	events := fs.String("events", "", "comma-separated event types (empty: all), e.g. new_release,drift_detected,agent_stale")
	mode := fs.String("mode", "instant", "instant, daily or weekly")
	services := fs.String("services", "", "only these services")
	owners := fs.String("owners", "", "only services of these owners")
	envs := fs.String("envs", "", "only these environments")
	minJump := fs.String("min-jump", "", "new releases: smallest jump to report (patch, minor, major)")
	digestHour := fs.Int("digest-hour", 8, "UTC hour digests go out")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *mode != "instant" && *mode != "daily" && *mode != "weekly" {
		return errors.New("-mode must be instant, daily or weekly")
	}
	db, ws, err := openDefault(ctx, *dsn)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	ch, err := db.GetChannelByName(ctx, ws.Scope(), *channel)
	if err != nil {
		return fmt.Errorf("channel %q: %w", *channel, err)
	}
	f := notifier.Filter{
		Services: splitList(*services), Owners: splitList(*owners), Environments: splitList(*envs),
		MinJump: versions.Jump(*minJump), DigestHour: digestHour,
	}
	raw, _ := json.Marshal(f)
	r, err := db.CreateRule(ctx, store.Rule{Scope: ws.Scope(), ChannelID: ch.ID, EventTypes: splitList(*events), Filter: raw, Mode: *mode})
	if err != nil {
		return err
	}
	cliAudit(ctx, db, ws, "notification_rule.create", "channel", ch.Name, "mode", r.Mode)
	_, _ = fmt.Fprintf(out, "notification rule %s created: %s → %s (%s)\n", r.ID, strings.Join(r.EventTypes, ","), ch.Name, r.Mode)
	return nil
}

func ackCmd(ctx context.Context, args []string, out io.Writer) error {
	fs, dsn := newFlags("ack")
	service := fs.String("service", "", "service name")
	kind := fs.String("kind", "release", "release or drift")
	untilVersion := fs.String("until-version", "", "quiet until this version (e.g. 2.1.0)")
	forDur := fs.Duration("for", 0, "quiet for this long (e.g. 336h for 14 days)")
	envName := fs.String("env", "", "only this environment (drift)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *untilVersion == "" && *forDur == 0 {
		return errors.New("-until-version or -for is required")
	}
	db, ws, err := openDefault(ctx, *dsn)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	svc, err := db.GetServiceByName(ctx, ws.Scope(), *service)
	if err != nil {
		return fmt.Errorf("service %q: %w", *service, err)
	}
	a := store.Ack{Scope: ws.Scope(), ServiceID: svc.ID, Kind: *kind, UntilVersion: *untilVersion, CreatedBy: "cli"}
	if *forDur > 0 {
		a.UntilAt = time.Now().Add(*forDur)
	}
	if *envName != "" {
		e, err := db.GetEnvironmentByName(ctx, ws.Scope(), *envName)
		if err != nil {
			return fmt.Errorf("environment %q: %w", *envName, err)
		}
		a.EnvironmentID = e.ID
	}
	if _, err := db.CreateAck(ctx, a); err != nil {
		return err
	}
	cliAudit(ctx, db, ws, "ack.create", "service", svc.Name, "kind", *kind)
	_, _ = fmt.Fprintf(out, "acknowledged %s %s\n", svc.Name, *kind)
	return nil
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
