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
	"bufio"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"golang.org/x/term"

	"github.com/pipozzz/goliash/internal/api"
	"github.com/pipozzz/goliash/internal/auth"
	"github.com/pipozzz/goliash/internal/collectors"
	"github.com/pipozzz/goliash/internal/collectors/compose"
	"github.com/pipozzz/goliash/internal/collectors/docker"
	"github.com/pipozzz/goliash/internal/collectors/ecs"
	"github.com/pipozzz/goliash/internal/collectors/kubernetes"
	"github.com/pipozzz/goliash/internal/collectors/nomad"
	"github.com/pipozzz/goliash/internal/collectors/swarm"
	"github.com/pipozzz/goliash/internal/demo"
	"github.com/pipozzz/goliash/internal/ingest"
	"github.com/pipozzz/goliash/internal/mapping"
	"github.com/pipozzz/goliash/internal/mcpserver"
	"github.com/pipozzz/goliash/internal/notifier"
	"github.com/pipozzz/goliash/internal/registry"
	"github.com/pipozzz/goliash/internal/store"
	"github.com/pipozzz/goliash/internal/tokens"
	"github.com/pipozzz/goliash/internal/ui"
	"github.com/pipozzz/goliash/internal/versions"
	"github.com/pipozzz/goliash/pkg/agentproto"
	"github.com/pipozzz/goliash/pkg/buildinfo"
)

const usage = `Usage:
  goliash [serve] [flags]                 run the server
  goliash env create -name NAME [-position N]
  goliash agent create -name NAME         prints the agent token once
  goliash agent list                      agents with status, version and last contact
  goliash agent rotate -name NAME         new token; the old one works until the agent uses the new one
  goliash agent revoke -name NAME         every token of the agent stops working
  goliash target create -agent NAME -env NAME -platform kubernetes|ecs|nomad|swarm|docker|compose -name NAME [-settings JSON] [-poll SECONDS]
  goliash matrix [-at 2026-09-12T14:00]   service × environment versions, now or as of a time
  goliash inventory [-at T] [-csv]        every running container with image and digest (audits)
  goliash hygiene                         moving tags, retagged images, untrusted registries, missing digests
  goliash mcp [-server URL] [-token T]    MCP server on stdin/stdout for AI assistants (env GOLIASH_URL, GOLIASH_TOKEN)
  goliash events [-service NAME] [-env NAME] [-since 2h] [-limit N]
  goliash drift                           open drifts
  goliash promotions                      versions waiting for the next environment, with their releases
  goliash delivery [-window 720h]         deploys per environment and lead times between environments
  goliash check [-service NAME]           check upstream registries now
  goliash service set -name NAME [-upstream REPO] [-owner O] [-kind own|third_party]
                      [-track patch|minor|major] [-pin-major N] [-tag-filter REGEXP] [-prerelease]
  goliash channel create -type slack|teams|gchat|discord|telegram|ntfy|grafana|webhook|email|push -name NAME [-url URL] [-secret S]
                         [-token T] [-chat-id ID] [-to a@b,c@d]
                         [-smtp-addr HOST:PORT -smtp-from ADDR [-smtp-username U] [-smtp-tls starttls|tls|none]]
  goliash channel test -name NAME
  goliash notify create -channel NAME [-events new_release,drift_detected] [-mode instant|daily|weekly]
                        [-services a,b] [-owners x] [-envs prod] [-min-jump minor] [-digest-hour 8]
  goliash ack -service NAME -kind release|drift [-until-version 2.1.0] [-for 336h] [-env prod]
  goliash workspace create -name N -slug S [-envs]   a workspace per client or team
  goliash workspace list
  goliash user grant -email E -role viewer|member|admin|none   access to the -workspace
  goliash user create -email E [-role owner|admin|member|viewer] [-name N]
  goliash user password -email E [-remove]   set a password (asked for, or one line on stdin)
  goliash user 2fa -email E -reset        turn two-factor sign-in off (lost phone, no recovery codes)
  goliash login-link -email E             one-time sign-in link (creates the first user as owner)
  goliash token create -name N [-role viewer|member] [-expires 90d]   API token for /api/v1, /metrics and /mcp (shown once)
  goliash token list
  goliash token revoke -name N
  goliash rule create -match image_repo|workload_name|label|ignore -pattern REGEXP [-service NAME] [-priority N]
  goliash backup -out DIR                 SQLite: a consistent copy of the database (and goliash.key) while the server runs
  goliash healthcheck                     exit 0 when the local server answers /healthz (container health checks)
  goliash demo                            fill the workspace with three weeks of example data
  goliash version

Every command takes -database (env GOLIASH_DATABASE_URL, default goliash.db) and
-workspace SLUG (env GOLIASH_WORKSPACE, default: the first workspace).
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
		if len(args) > 0 && cmd == "agent" && (args[0] == "list" || args[0] == "rotate" || args[0] == "revoke") {
			cmd, args = "agent "+args[0], args[1:]
		}
		if len(args) > 0 && cmd == "token" && (args[0] == "list" || args[0] == "revoke") {
			cmd, args = "token "+args[0], args[1:]
		}
		if len(args) > 0 && ((cmd == "user" && (args[0] == "grant" || args[0] == "password" || args[0] == "2fa")) || (cmd == "workspace" && (args[0] == "create" || args[0] == "list"))) {
			cmd, args = cmd+" "+args[0], args[1:]
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
	case "promotions":
		return promotionsCmd(ctx, args, out)
	case "delivery":
		return deliveryCmd(ctx, args, out)
	case "inventory":
		return inventoryCmd(ctx, args, out)
	case "hygiene":
		return hygieneCmd(ctx, args, out)
	case "mcp":
		return mcpCmd(ctx, args)
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
	case "user 2fa":
		return userTwoFactor(ctx, args, out)
	case "user password":
		return userPassword(ctx, args, os.Stdin, out)
	case "user grant":
		return userGrant(ctx, args, out)
	case "workspace create":
		return workspaceCreate(ctx, args, out)
	case "workspace list":
		return workspaceList(ctx, args, out)
	case "login-link":
		return loginLink(ctx, args, out)
	case "token create":
		return tokenCreate(ctx, args, out)
	case "agent list":
		return agentList(ctx, args, out)
	case "agent rotate":
		return agentRotate(ctx, args, out)
	case "agent revoke":
		return agentRevoke(ctx, args, out)
	case "token list":
		return tokenList(ctx, args, out)
	case "token revoke":
		return tokenRevoke(ctx, args, out)
	case "demo":
		return demoCmd(ctx, args, out)
	case "backup":
		return backupCmd(ctx, args, out)
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

// cliWorkspace is the -workspace flag every command takes.
var cliWorkspace string

func newFlags(name string) (*flag.FlagSet, *string) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	dsn := fs.String("database", envOr("GOLIASH_DATABASE_URL", "goliash.db"),
		"SQLite file path or postgres:// URL (env GOLIASH_DATABASE_URL)")
	fs.StringVar(&cliWorkspace, "workspace", os.Getenv("GOLIASH_WORKSPACE"),
		"workspace slug (env GOLIASH_WORKSPACE); default: the first workspace")
	return fs, dsn
}

// openDefault opens the database and returns the default workspace.
func openDefault(ctx context.Context, dsn string) (*store.Store, store.Workspace, error) {
	db, err := store.Open(ctx, dsn)
	if err != nil {
		return nil, store.Workspace{}, err
	}
	if err := useSecretKey(ctx, db, dsn); err != nil {
		_ = db.Close()
		return nil, store.Workspace{}, err
	}
	ws, err := db.EnsureDefaultWorkspace(ctx)
	if err != nil {
		_ = db.Close()
		return nil, store.Workspace{}, fmt.Errorf("default workspace: %w", err)
	}
	if cliWorkspace != "" {
		if ws, err = db.GetWorkspaceBySlug(ctx, ws.OrgID, cliWorkspace); err != nil {
			_ = db.Close()
			return nil, store.Workspace{}, fmt.Errorf("workspace %q: %w (see goliash workspace list)", cliWorkspace, err)
		}
	}
	return db, ws, nil
}

// useSecretKey gives the store the key that encrypts notification channel secrets:
// GOLIASH_SECRET_KEY or the file in GOLIASH_SECRET_KEY_FILE (32 bytes, base64 or hex),
// else goliash.key next to a SQLite database, created on first use. PostgreSQL without
// a key keeps secrets in the clear, with a warning.
func useSecretKey(ctx context.Context, db *store.Store, dsn string) error {
	key, source, err := secretKey(dsn)
	if err != nil {
		return err
	}
	if key == nil {
		slog.Warn("notification channel secrets are stored unencrypted; set GOLIASH_SECRET_KEY")
	} else {
		if err := db.SetSecretKey(key); err != nil {
			return fmt.Errorf("%s: %w", source, err)
		}
		if n, err := db.SealSecrets(ctx); err != nil {
			return fmt.Errorf("encrypt stored secrets: %w", err)
		} else if n > 0 {
			slog.Info("encrypted stored channel secrets", "channels", n, "key", source)
		}
	}
	if err := db.CheckSecrets(ctx); err != nil {
		return fmt.Errorf("stored secrets: %w", err)
	}
	return nil
}

func secretKey(dsn string) (key []byte, source string, err error) {
	if v := os.Getenv("GOLIASH_SECRET_KEY"); v != "" {
		key, err = decodeKey(v)
		return key, "GOLIASH_SECRET_KEY", err
	}
	if path := os.Getenv("GOLIASH_SECRET_KEY_FILE"); path != "" {
		b, err := os.ReadFile(path) //nolint:gosec // the operator names the file
		if err != nil {
			return nil, "", fmt.Errorf("GOLIASH_SECRET_KEY_FILE: %w", err)
		}
		key, err = decodeKey(string(b))
		return key, path, err
	}
	if strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://") {
		return nil, "", nil
	}
	dbPath := strings.TrimPrefix(dsn, "sqlite://")
	if dbPath == ":memory:" {
		return nil, "", nil
	}
	path := filepath.Join(filepath.Dir(dbPath), "goliash.key")
	if b, err := os.ReadFile(path); err == nil { //nolint:gosec // next to the database the operator chose
		key, err = decodeKey(string(b))
		return key, path, err
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, "", err
	}
	key = make([]byte, store.SecretKeySize)
	_, _ = rand.Read(key)
	if err := os.WriteFile(path, []byte(base64.StdEncoding.EncodeToString(key)+"\n"), 0o600); err != nil {
		return nil, "", fmt.Errorf("create secret key: %w", err)
	}
	slog.Info("created a secret key for channel secrets; back it up with the database", "path", path)
	return key, path, nil
}

func decodeKey(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	for _, dec := range []func(string) ([]byte, error){base64.StdEncoding.DecodeString, base64.RawURLEncoding.DecodeString, hex.DecodeString} {
		if b, err := dec(s); err == nil && len(b) == store.SecretKeySize {
			return b, nil
		}
	}
	return nil, fmt.Errorf("secret key must be %d bytes as base64 or hex (openssl rand -base64 32)", store.SecretKeySize)
}

func serve(ctx context.Context, args []string) error {
	fs, dsn := newFlags("serve")
	listen := fs.String("listen", envOr("GOLIASH_LISTEN", ":8080"), "HTTP listen address (env GOLIASH_LISTEN)")
	upstreamEvery := fs.Duration("upstream-interval", time.Hour, "how often public registries are checked for new tags")
	collect := fs.Bool("collect", true, "collect targets that have no agent in the server itself")
	eol := fs.Bool("eol", true, "report end-of-life release cycles from endoflife.date")
	keepSnapshots := fs.Int("keep-snapshots", 20, "processed snapshots kept per target; older ones are deleted hourly")
	publicURL := fs.String("public-url", envOr("GOLIASH_PUBLIC_URL", "http://localhost:8080"),
		"URL people use to reach this server, for sign-in links and cookies (env GOLIASH_PUBLIC_URL)")
	debug := fs.Bool("debug", os.Getenv("GOLIASH_DEBUG") != "", "debug logging (env GOLIASH_DEBUG)")
	logFormat := fs.String("log-format", envOr("GOLIASH_LOG_FORMAT", "text"), "text or json (env GOLIASH_LOG_FORMAT)")
	drain := fs.Duration("drain", envDuration("GOLIASH_DRAIN", 0),
		"on shutdown, answer /readyz with 503 this long before closing, so load balancers stop sending (env GOLIASH_DRAIN)")
	backupDir := fs.String("backup-dir", os.Getenv("GOLIASH_BACKUP_DIR"), "SQLite: write a backup here every day (env GOLIASH_BACKUP_DIR)")
	backupKeep := fs.Int("backup-keep", envInt("GOLIASH_BACKUP_KEEP", 7), "backups kept in -backup-dir (env GOLIASH_BACKUP_KEEP)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	// "goliash -database x.db matrix" would otherwise start a server instead of the command.
	if fs.NArg() > 0 {
		return fmt.Errorf("unexpected %q: flags go after the command, e.g. goliash %s -database …", fs.Arg(0), fs.Arg(0))
	}

	level := slog.LevelInfo
	if *debug {
		level = slog.LevelDebug
	}
	log, err := newLogger(*logFormat, level)
	if err != nil {
		return err
	}
	slog.SetDefault(log)

	db, ws, err := openDefault(ctx, *dsn)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	log.Info("database ready", "dialect", db.Dialect(), "workspace", ws.Slug)
	if boot, err := loadBootstrap(); err != nil {
		return err
	} else if boot != nil {
		if err := applyBootstrap(ctx, db, ws, boot, *publicURL, log); err != nil {
			return fmt.Errorf("bootstrap: %w", err)
		}
	}
	if n, err := db.CountUsers(ctx, ws.OrgID); err == nil && n == 0 {
		a, err := auth.New(db, log, *publicURL, nil)
		if err != nil {
			return err
		}
		link, err := a.SetupLink(ctx)
		if err != nil {
			return err
		}
		log.Warn("nobody has an account yet: open this link to create the first one (it works once, for 24 hours; restart for a new one)",
			"link", link)
	}
	if email := strings.TrimSpace(os.Getenv("GOLIASH_RECOVERY_EMAIL")); email != "" {
		if err := recoveryLink(ctx, db, ws, email, *publicURL, log); err != nil {
			return fmt.Errorf("recovery: %w", err)
		}
	}

	svc := ingest.New(db, log)
	checker := versions.NewChecker(db, registry.New(), log, *upstreamEvery)
	checker.SetGitHub(versions.NewGitHub(os.Getenv("GOLIASH_GITHUB_TOKEN")))
	checker.SetGitLab(versions.NewGitLab(os.Getenv("GOLIASH_GITLAB_URL"), os.Getenv("GOLIASH_GITLAB_TOKEN")))
	if *eol {
		checker.SetEOL(versions.NewEOL())
	}
	versions.SetAllowedRegistries(splitList(os.Getenv("GOLIASH_ALLOWED_REGISTRIES")))
	svc.SetUpstreams(checker)
	notify := notifier.New(db, log, senders(db, *publicURL))
	notify.SetPublicURL(*publicURL)
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
	leader := db.NewLeader(log)
	public := api.NewPublicHandler(db, authn, log)
	public.SetLeader(leader.IsLeader)
	public.Register(mux)
	ui.New(ui.Options{
		Store: db, Auth: authn, Checker: checker, Notifier: notify, Hub: hub, Log: log, PublicURL: *publicURL,
		SMTP: smtpFromEnv().Addr != "",
	}).Register(mux)
	mux.Handle("/mcp", mcpserver.HTTPHandler(loopbackURL(*listen)))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		if err := db.Ping(r.Context()); err != nil {
			http.Error(w, "database unavailable", http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte("ok\n"))
	})
	// Readiness: whether to send this server requests. It says no while shutting
	// down, so load balancers move traffic away before connections close.
	var draining atomic.Bool
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		if draining.Load() {
			http.Error(w, "shutting down", http.StatusServiceUnavailable)
			return
		}
		pctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if err := db.Ping(pctx); err != nil {
			http.Error(w, "database unavailable", http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte("ok\n"))
	})

	// Every server serves the UI, the API and agents; one, the leader, runs the
	// background work. With PostgreSQL several servers may share the database: changes
	// and new snapshots reach the others through LISTEN/NOTIFY.
	if db.Clustered() {
		hub.SetRelay(func(ws string) error { return db.Notify(context.WithoutCancel(ctx), channelChanged, ws) })
		svc.OnArrive(func() { _ = db.Notify(context.WithoutCancel(ctx), channelSnapshot, "") })
		go db.Listen(ctx, []string{channelChanged, channelSnapshot}, log, func(channel, payload string) {
			switch channel {
			case channelChanged:
				hub.PublishLocal(payload)
			case channelSnapshot:
				if leader.IsLeader() {
					svc.Wake()
				}
			}
		})
	}
	go leader.Run(ctx, func(lctx context.Context) {
		go svc.WatchStale(lctx, time.Minute, notify.AgentStale)
		go notify.Run(lctx, 15*time.Second)
		go svc.RunProcessor(lctx, 10*time.Second)
		go checker.Run(lctx, *upstreamEvery, time.Minute)
		go housekeeping(lctx, db, log, *keepSnapshots)
		if *backupDir != "" {
			go backups(lctx, db, *dsn, *backupDir, *backupKeep, log)
		}
		if *collect {
			go ingest.NewServerCollectors(svc, db, map[agentproto.Platform]collectors.Factory{
				agentproto.Kubernetes: kubernetes.New,
				agentproto.Ecs:        ecs.New,
				agentproto.Nomad:      nomad.New,
				agentproto.Swarm:      swarm.New,
				agentproto.Docker:     docker.New,
				agentproto.Compose:    compose.NewRemote, // URLs only: no reading the server's files
			}, log).Run(lctx, time.Minute)
		}
	})

	srv := &http.Server{
		Addr: *listen,
		// Browsers may not send state-changing requests from other origins (CSRF);
		// agents and API clients send no Origin and are unaffected.
		Handler: ui.SecurityHeaders(ui.LimitBodies(http.NewCrossOriginProtection().Handler(mux), 1<<20, "/agent/"),
			strings.HasPrefix(*publicURL, "https://")),
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
	draining.Store(true)
	if *drain > 0 {
		log.Info("draining before shutdown", "for", *drain)
		time.Sleep(*drain)
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

// agentByName opens the database and finds an agent for the agent subcommands.
func agentByName(ctx context.Context, cmd string, args []string) (*store.Store, store.Workspace, store.Agent, error) {
	fs, dsn := newFlags(cmd)
	name := fs.String("name", "", "agent name")
	if err := fs.Parse(args); err != nil {
		return nil, store.Workspace{}, store.Agent{}, err
	}
	if *name == "" {
		return nil, store.Workspace{}, store.Agent{}, errors.New("-name is required")
	}
	db, ws, err := openDefault(ctx, *dsn)
	if err != nil {
		return nil, store.Workspace{}, store.Agent{}, err
	}
	a, err := db.GetAgentByName(ctx, ws.Scope(), *name)
	if errors.Is(err, store.ErrNotFound) {
		_ = db.Close()
		return nil, store.Workspace{}, store.Agent{}, fmt.Errorf("no agent %s in workspace %s", *name, ws.Slug)
	}
	if err != nil {
		_ = db.Close()
		return nil, store.Workspace{}, store.Agent{}, err
	}
	return db, ws, a, nil
}

func agentList(ctx context.Context, args []string, out io.Writer) error {
	fs, dsn := newFlags("agent list")
	if err := fs.Parse(args); err != nil {
		return err
	}
	db, ws, err := openDefault(ctx, *dsn)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	agents, err := db.ListAgents(ctx, ws.Scope())
	if err != nil {
		return err
	}
	tw := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "NAME\tSTATUS\tVERSION\tHOST\tLAST SEEN")
	for _, a := range agents {
		status := "online"
		switch {
		case a.ActiveTokens == 0:
			status = "revoked"
		case a.LastSeenAt.IsZero():
			status = "never"
		case !a.StaleSince.IsZero():
			status = "stale"
		}
		if a.ActiveTokens > 1 {
			status += " (rotation pending)"
		}
		seen := "never"
		if !a.LastSeenAt.IsZero() {
			seen = a.LastSeenAt.UTC().Format(time.DateTime)
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", a.Name, status, a.Version, a.Hostname, seen)
	}
	return tw.Flush()
}

func agentRotate(ctx context.Context, args []string, out io.Writer) error {
	db, ws, a, err := agentByName(ctx, "agent rotate", args)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	token, hash := tokens.New(tokens.Agent)
	if err := db.AddAgentToken(ctx, ws.Scope(), a.ID, hash); err != nil {
		return err
	}
	cliAudit(ctx, db, ws, "agent.rotate_token", "agent", a.Name)
	_, _ = fmt.Fprintf(out, "New token for %s (shown once). The old token works until the agent first uses this one:\n%s\n", a.Name, token)
	return nil
}

func agentRevoke(ctx context.Context, args []string, out io.Writer) error {
	db, ws, a, err := agentByName(ctx, "agent revoke", args)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	if err := db.RevokeAgentTokens(ctx, ws.Scope(), a.ID); err != nil {
		return err
	}
	cliAudit(ctx, db, ws, "agent.revoke", "agent", a.Name)
	_, _ = fmt.Fprintf(out, "every token of %s revoked; goliash agent rotate gives it a new one\n", a.Name)
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
	platform := fs.String("platform", "", "kubernetes, ecs, nomad, swarm, docker or compose")
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
	asOf := fs.String("at", "", "show what ran at this time (UTC), e.g. 2026-09-12T14:00")
	if err := fs.Parse(args); err != nil {
		return err
	}
	db, ws, err := openDefault(ctx, *dsn)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	var o versions.Overview
	if *asOf != "" {
		at, perr := api.ParseAt(*asOf)
		if perr != nil {
			return perr
		}
		o, err = versions.OverviewAt(ctx, db, ws.Scope(), at)
		_, _ = fmt.Fprintf(out, "As of %s UTC\n\n", at.UTC().Format("2006-01-02 15:04"))
	} else {
		o, err = versions.LoadOverview(ctx, db, ws.Scope())
	}
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
		} else if ref := o.Refs[row.Service.ID]; versions.CheckedByAgent(row.Service, ref.Repo) {
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
			case "declared":
				detail = fmt.Sprintf("%s, Git declares %s", det.Running, det.Other)
			case "eol":
				detail = fmt.Sprintf("%s, cycle %s end of life %s", det.Running, det.Other, det.EOL)
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
	checker.SetGitHub(versions.NewGitHub(os.Getenv("GOLIASH_GITHUB_TOKEN")))
	checker.SetGitLab(versions.NewGitLab(os.Getenv("GOLIASH_GITLAB_URL"), os.Getenv("GOLIASH_GITLAB_TOKEN")))
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
	envName := fs.String("env", "", "only events in this environment")
	since := fs.Duration("since", 0, "only events of the last duration, e.g. 2h (what changed before an incident)")
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
	if *since > 0 {
		f.Since = time.Now().Add(-*since)
	}
	if *service != "" {
		svc, err := db.GetServiceByName(ctx, sc, *service)
		if err != nil {
			return fmt.Errorf("service %q: %w", *service, err)
		}
		f.ServiceID = svc.ID
	}
	if *envName != "" {
		env, err := db.GetEnvironmentByName(ctx, sc, *envName)
		if err != nil {
			return fmt.Errorf("environment %q: %w", *envName, err)
		}
		f.EnvironmentID = env.ID
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
	a.SetPasswordLogin(os.Getenv("GOLIASH_PASSWORD_LOGIN") != "false")
	a.SetSessionTTL(envDuration("GOLIASH_SESSION_TTL", 30*24*time.Hour))
	db.SetSessionIdle(envDuration("GOLIASH_SESSION_IDLE", 14*24*time.Hour))
	a.SetTrustProxy(os.Getenv("GOLIASH_TRUST_PROXY") == "true")
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
	role := fs.String("role", store.RoleViewer, "owner or admin (whole organization), member or viewer (the -workspace)")
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
	if !store.OrgWide(u.Role) {
		if err := db.SetMembership(ctx, u.ID, ws.ID, u.Role); err != nil {
			return err
		}
		_, _ = fmt.Fprintf(out, "%s may open workspace %s; grant more with goliash user grant\n", u.Email, ws.Slug)
	}
	cliAudit(ctx, db, ws, "user.create", "user", u.Email, "role", u.Role)
	_, _ = fmt.Fprintf(out, "user %s created with role %s\n", u.Email, u.Role)
	return nil
}

func userGrant(ctx context.Context, args []string, out io.Writer) error {
	fs, dsn := newFlags("user grant")
	email := fs.String("email", "", "e-mail address of an existing user")
	role := fs.String("role", store.RoleViewer, "viewer, member, admin, or none to take access away")
	if err := fs.Parse(args); err != nil {
		return err
	}
	db, ws, err := openDefault(ctx, *dsn)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	u, err := db.GetUserByEmail(ctx, ws.OrgID, *email)
	if err != nil {
		return fmt.Errorf("user %q: %w", *email, err)
	}
	if store.OrgWide(u.Role) {
		return fmt.Errorf("%s is %s of the organization and already reaches every workspace", u.Email, u.Role)
	}
	switch *role {
	case store.RoleViewer, store.RoleMember, store.RoleAdmin:
		err = db.SetMembership(ctx, u.ID, ws.ID, *role)
	case "none":
		err = db.RemoveMembership(ctx, u.ID, ws.ID)
	default:
		return errors.New("-role must be viewer, member, admin or none")
	}
	if err != nil {
		return err
	}
	cliAudit(ctx, db, ws, "user.role", "user", u.Email, "to", *role, "workspace", ws.Slug)
	_, _ = fmt.Fprintf(out, "%s: %s in %s\n", u.Email, *role, ws.Slug)
	return nil
}

func workspaceCreate(ctx context.Context, args []string, out io.Writer) error {
	fs, dsn := newFlags("workspace create")
	name := fs.String("name", "", "display name, e.g. Client A")
	slug := fs.String("slug", "", "short id, e.g. client-a")
	envs := fs.Bool("envs", false, "also create dev, staging and prod")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *name == "" || *slug == "" {
		return errors.New("-name and -slug are required")
	}
	db, def, err := openDefault(ctx, *dsn)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	ws, err := db.CreateWorkspace(ctx, def.OrgID, *name, strings.ToLower(*slug))
	if err != nil {
		return err
	}
	if *envs {
		for i, env := range []string{"dev", "staging", "prod"} {
			if _, err := db.CreateEnvironment(ctx, ws.Scope(), env, (i+1)*10); err != nil {
				return err
			}
		}
	}
	_ = db.Audit(ctx, store.AuditEntry{
		OrgID: ws.OrgID, Actor: "cli", Action: "workspace.create",
		Details: map[string]string{"workspace": ws.Slug, "name": ws.Name},
	})
	_, _ = fmt.Fprintf(out, "workspace %s created; use -workspace %s with other commands\n", ws.Name, ws.Slug)
	return nil
}

func workspaceList(ctx context.Context, args []string, out io.Writer) error {
	fs, dsn := newFlags("workspace list")
	if err := fs.Parse(args); err != nil {
		return err
	}
	db, def, err := openDefault(ctx, *dsn)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	all, err := db.ListOrgWorkspaces(ctx, def.OrgID)
	if err != nil {
		return err
	}
	tw := tabwriter.NewWriter(out, 0, 0, 3, ' ', 0)
	_, _ = fmt.Fprintln(tw, "SLUG\tNAME\tTARGETS\tSERVICES\tMEMBERS")
	for _, w := range all {
		c, err := db.CountWorkspace(ctx, w.ID)
		if err != nil {
			return err
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%d\t%d\t%d\n", w.Slug, w.Name, c.Targets, c.Services, c.Members)
	}
	return tw.Flush()
}

// userTwoFactor turns someone's two-factor sign-in off and signs them out everywhere.
func userTwoFactor(ctx context.Context, args []string, out io.Writer) error {
	fs, dsn := newFlags("user 2fa")
	email := fs.String("email", "", "e-mail address of the user")
	reset := fs.Bool("reset", false, "turn two-factor sign-in off")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *email == "" || !*reset {
		return errors.New("-email and -reset are required")
	}
	db, ws, err := openDefault(ctx, *dsn)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	u, err := db.GetUserByEmail(ctx, ws.OrgID, *email)
	if errors.Is(err, store.ErrNotFound) {
		return fmt.Errorf("no user %s", *email)
	}
	if err != nil {
		return err
	}
	if err := db.DisableTOTP(ctx, u.ID); err != nil {
		return err
	}
	if _, err := db.DeleteUserSessions(ctx, u.ID, ""); err != nil {
		return err
	}
	cliAudit(ctx, db, ws, "user.2fa_reset", "user", u.Email)
	_, _ = fmt.Fprintf(out, "two-factor sign-in of %s is off; every device signed out\n", u.Email)
	return nil
}

// userPassword sets or removes a user's password and signs them out everywhere.
// It asks for the password on a terminal, else reads one line from stdin.
func userPassword(ctx context.Context, args []string, in *os.File, out io.Writer) error {
	fs, dsn := newFlags("user password")
	email := fs.String("email", "", "e-mail address of the user")
	remove := fs.Bool("remove", false, "remove the password (they sign in with a link or single sign-on)")
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
		return fmt.Errorf("no user %s; create one with goliash user create", *email)
	}
	if err != nil {
		return err
	}
	if *remove {
		if err := db.SetUserPassword(ctx, u.ID, "", ""); err != nil {
			return err
		}
		cliAudit(ctx, db, ws, "user.password_remove", "user", u.Email)
		_, _ = fmt.Fprintf(out, "password of %s removed; every device signed out\n", u.Email)
		return nil
	}
	password, err := readPassword(in, out)
	if err != nil {
		return err
	}
	if err := auth.CheckPassword(password, u.Email); err != nil {
		return err
	}
	if err := db.SetUserPassword(ctx, u.ID, auth.HashPassword(password), ""); err != nil {
		return err
	}
	cliAudit(ctx, db, ws, "user.password_set", "user", u.Email)
	_, _ = fmt.Fprintf(out, "password of %s set; every device signed out\n", u.Email)
	return nil
}

func readPassword(in *os.File, out io.Writer) (string, error) {
	if !term.IsTerminal(int(in.Fd())) { //nolint:gosec // a file descriptor fits in int
		line, err := bufio.NewReader(in).ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return "", err
		}
		return strings.TrimRight(line, "\r\n"), nil
	}
	_, _ = fmt.Fprint(out, "New password: ")
	first, err := term.ReadPassword(int(in.Fd())) //nolint:gosec // a file descriptor fits in int
	_, _ = fmt.Fprintln(out)
	if err != nil {
		return "", err
	}
	_, _ = fmt.Fprint(out, "Repeat it: ")
	second, err := term.ReadPassword(int(in.Fd())) //nolint:gosec // a file descriptor fits in int
	_, _ = fmt.Fprintln(out)
	if err != nil {
		return "", err
	}
	if string(first) != string(second) {
		return "", errors.New("the two passwords differ")
	}
	return string(first), nil
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
	role := fs.String("role", store.RoleViewer, "viewer reads; member also acknowledges")
	expires := fs.String("expires", "", "lifetime, e.g. 90d or 720h (default: never)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *name == "" {
		return errors.New("-name is required")
	}
	if *role != store.RoleViewer && *role != store.RoleMember {
		return errors.New("-role must be viewer or member")
	}
	t := store.APIToken{Name: *name, Role: *role, CreatedBy: "cli"}
	if *expires != "" {
		d, err := parseLifetime(*expires)
		if err != nil {
			return err
		}
		t.ExpiresAt = time.Now().Add(d)
	}
	db, ws, err := openDefault(ctx, *dsn)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	token, hash := tokens.New(tokens.API)
	if _, err := db.CreateAPIToken(ctx, ws.Scope(), t, hash); err != nil {
		return err
	}
	exp := "never"
	if !t.ExpiresAt.IsZero() {
		exp = t.ExpiresAt.UTC().Format(time.DateOnly)
	}
	cliAudit(ctx, db, ws, "api_token.create", "token", *name, "role", *role, "expires", exp)
	_, _ = fmt.Fprintf(out, "API token %s (%s, expires %s; shown once):\n%s\n", *name, *role, exp, token)
	return nil
}

// parseLifetime reads a duration with an optional day unit: 90d, 36h, 1d12h.
func parseLifetime(s string) (time.Duration, error) {
	var days int
	if i := strings.Index(s, "d"); i > 0 {
		n, err := strconv.Atoi(s[:i])
		if err != nil {
			return 0, fmt.Errorf("bad lifetime %q", s)
		}
		days, s = n, s[i+1:]
	}
	var rest time.Duration
	if s != "" {
		var err error
		if rest, err = time.ParseDuration(s); err != nil {
			return 0, fmt.Errorf("bad lifetime %q: use e.g. 90d or 720h", s)
		}
	}
	d := time.Duration(days)*24*time.Hour + rest
	if d <= 0 || d > 10*365*24*time.Hour {
		return 0, errors.New("the lifetime must be between a moment and ten years")
	}
	return d, nil
}

func tokenList(ctx context.Context, args []string, out io.Writer) error {
	fs, dsn := newFlags("token list")
	if err := fs.Parse(args); err != nil {
		return err
	}
	db, ws, err := openDefault(ctx, *dsn)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	toks, err := db.ListAPITokens(ctx, ws.Scope())
	if err != nil {
		return err
	}
	tw := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "NAME\tROLE\tCREATED\tBY\tLAST USED\tEXPIRES")
	day := func(t time.Time, zero string) string {
		if t.IsZero() {
			return zero
		}
		return t.UTC().Format(time.DateOnly)
	}
	now := time.Now()
	for _, t := range toks {
		exp := day(t.ExpiresAt, "never")
		if t.Expired(now) {
			exp += " (expired)"
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", t.Name, t.Role, day(t.CreatedAt, ""), t.CreatedBy, day(t.LastUsed, "never"), exp)
	}
	return tw.Flush()
}

func tokenRevoke(ctx context.Context, args []string, out io.Writer) error {
	fs, dsn := newFlags("token revoke")
	name := fs.String("name", "", "name of the token")
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
	toks, err := db.ListAPITokens(ctx, ws.Scope())
	if err != nil {
		return err
	}
	var match []store.APIToken
	for _, t := range toks {
		if t.Name == *name {
			match = append(match, t)
		}
	}
	switch len(match) {
	case 0:
		return fmt.Errorf("no API token %s in workspace %s", *name, ws.Slug)
	case 1:
	default:
		return fmt.Errorf("%d tokens are named %s; revoke them on the Users page", len(match), *name)
	}
	if err := db.RevokeAPIToken(ctx, ws.Scope(), match[0].ID); err != nil {
		return err
	}
	cliAudit(ctx, db, ws, "api_token.revoke", "token", *name)
	_, _ = fmt.Fprintf(out, "API token %s revoked\n", *name)
	return nil
}

// housekeeping deletes data nothing reads any more, hourly.
// Notification channels between servers sharing a PostgreSQL database.
const (
	channelChanged  = "goliash_changed"  // payload: workspace ID; browsers reload parts
	channelSnapshot = "goliash_snapshot" // a snapshot arrived; the leader processes it
)

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
	db, ws, err := openDefault(ctx, *dsn)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	if err := demo.Seed(ctx, db, ws, slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
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

// senders are the notification senders: every channel type, web push included.
func senders(db *store.Store, publicURL string) map[string]notifier.Sender {
	hc := &http.Client{Timeout: 30 * time.Second}
	out := notifier.DefaultSenders(hc, smtpFromEnv())
	// Push services want to know who sends: an https URL or a mailto: address.
	subject := envOr("GOLIASH_PUSH_SUBJECT", publicURL)
	if !strings.HasPrefix(subject, "https://") && !strings.HasPrefix(subject, "mailto:") {
		subject = "mailto:goliash@localhost"
	}
	out["push"] = notifier.WebPush{Store: db, HTTP: hc, Subject: subject}
	return out
}

func smtpFromEnv() notifier.SMTPConfig {
	return notifier.SMTPConfig{
		Addr:     os.Getenv("GOLIASH_SMTP_ADDR"),
		Username: os.Getenv("GOLIASH_SMTP_USERNAME"),
		Password: os.Getenv("GOLIASH_SMTP_PASSWORD"),
		From:     os.Getenv("GOLIASH_SMTP_FROM"),
		TLS:      os.Getenv("GOLIASH_SMTP_TLS"),
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
	typ := fs.String("type", "", "slack, teams, gchat, discord, telegram, ntfy, grafana, webhook, email or push")
	name := fs.String("name", "", "channel name")
	url := fs.String("url", "", "webhook URL (Slack, Teams workflow, Google Chat, Discord, webhook), or an ntfy topic URL")
	secret := fs.String("secret", "", "webhook signing secret (HMAC-SHA256)")
	token := fs.String("token", "", "Telegram bot token, Grafana service account token, or ntfy access token")
	chatID := fs.String("chat-id", "", "Telegram chat ID")
	to := fs.String("to", "", "comma-separated e-mail recipients")
	smtpAddr := fs.String("smtp-addr", "", "the e-mail channel's own mail server, host:port (default: GOLIASH_SMTP_ADDR)")
	smtpFrom := fs.String("smtp-from", "", "sender address for -smtp-addr")
	smtpUser := fs.String("smtp-username", "", "user name for -smtp-addr")
	smtpTLS := fs.String("smtp-tls", "", "starttls, tls or none (default: tls on port 465, else starttls)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg := map[string]any{}
	switch *typ {
	case "slack", "webhook", "discord", "ntfy", "teams", "gchat":
		if *url == "" {
			return errors.New("-url is required")
		}
		cfg["url"] = *url
		if *secret != "" && *typ == "webhook" {
			cfg["secret"] = *secret
		}
		if *token != "" && *typ == "ntfy" {
			cfg["token"] = *token
		}
	case "grafana":
		if *url == "" || *token == "" {
			return errors.New("-url (Grafana) and -token (service account token) are required")
		}
		cfg["url"], cfg["token"] = *url, *token
	case "telegram":
		if *token == "" || *chatID == "" {
			return errors.New("-token and -chat-id are required")
		}
		cfg["bot_token"], cfg["chat_id"] = *token, *chatID
	case "push":
		// Browsers subscribe from the Notifications page.
	case "email":
		if *to == "" {
			return errors.New("-to is required")
		}
		cfg["to"] = splitList(*to)
		if *smtpAddr != "" {
			if *smtpFrom == "" {
				return errors.New("-smtp-from is required with -smtp-addr")
			}
			cfg["smtp_addr"], cfg["smtp_from"] = *smtpAddr, *smtpFrom
			if *smtpUser != "" {
				// The password comes from the environment, not argv (visible in ps).
				cfg["smtp_username"], cfg["smtp_password"] = *smtpUser, os.Getenv("GOLIASH_CHANNEL_SMTP_PASSWORD")
			}
			if *smtpTLS != "" {
				cfg["smtp_tls"] = *smtpTLS
			}
		}
	default:
		return errors.New("-type must be slack, teams, gchat, discord, telegram, ntfy, grafana, webhook, email or push")
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
	n := notifier.New(db, slog.New(slog.NewTextHandler(io.Discard, nil)), senders(db, envOr("GOLIASH_PUBLIC_URL", "")))
	if err := n.SendTest(ctx, ch, ws.Name); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(out, "test notification sent to %s\n", ch.Name)
	return nil
}

func notifyCreate(ctx context.Context, args []string, out io.Writer) error {
	fs, dsn := newFlags("notify create")
	channel := fs.String("channel", "", "channel name")
	events := fs.String("events", "", "comma-separated event types (empty: all events), e.g. new_release,drift_detected,agent_stale; updates_plan for the upgrade plan")
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

func promotionsCmd(ctx context.Context, args []string, out io.Writer) error {
	fs, dsn := newFlags("promotions")
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
	list, err := versions.Promotions(ctx, db, ws.Scope(), o)
	if err != nil {
		return err
	}
	if len(list) == 0 {
		_, _ = fmt.Fprintln(out, "Nothing waits: every environment runs what the one before it runs.")
		return nil
	}
	tw := tabwriter.NewWriter(out, 0, 0, 3, ' ', 0)
	_, _ = fmt.Fprintln(tw, "SERVICE\tPROMOTE\tFROM → TO\tWAITING\tRELEASES")
	for _, p := range list {
		var rel []string
		for _, r := range p.Releases {
			rel = append(rel, r.Version)
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s → %s\t%s → %s\t%s\t%s\n", p.Service.Name, p.Running, p.Version, p.From.Name, p.To.Name,
			time.Since(p.Since).Round(time.Hour), strings.Join(rel, ", "))
	}
	return tw.Flush()
}

func deliveryCmd(ctx context.Context, args []string, out io.Writer) error {
	fs, dsn := newFlags("delivery")
	window := fs.Duration("window", 30*24*time.Hour, "look back this long")
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
	stats, err := versions.DeliveryStats(ctx, db, ws.Scope(), o, *window, time.Now())
	if err != nil {
		return err
	}
	tw := tabwriter.NewWriter(out, 0, 0, 3, ' ', 0)
	header := []string{"SERVICE"}
	for _, e := range o.Matrix.Environments {
		header = append(header, strings.ToUpper(e.Name)+" DEPLOYS")
	}
	for i := 1; i < len(o.Matrix.Environments); i++ {
		header = append(header, strings.ToUpper(o.Matrix.Environments[i-1].Name+"→"+o.Matrix.Environments[i].Name))
	}
	_, _ = fmt.Fprintln(tw, strings.Join(header, "\t"))
	for _, d := range stats {
		cols := []string{d.Service.Name}
		for _, e := range d.Envs {
			cols = append(cols, strconv.Itoa(e.Deploys))
		}
		for _, lt := range d.LeadTimes {
			if lt.Samples == 0 {
				cols = append(cols, "-")
			} else {
				cols = append(cols, versions.HumanDuration(lt.Median))
			}
		}
		_, _ = fmt.Fprintln(tw, strings.Join(cols, "\t"))
	}
	return tw.Flush()
}

func inventoryCmd(ctx context.Context, args []string, out io.Writer) error {
	fs, dsn := newFlags("inventory")
	asOf := fs.String("at", "", "what ran at this time (UTC), e.g. 2026-09-12T14:00")
	asCSV := fs.Bool("csv", false, "write CSV")
	if err := fs.Parse(args); err != nil {
		return err
	}
	var at *time.Time
	if *asOf != "" {
		t, err := api.ParseAt(*asOf)
		if err != nil {
			return err
		}
		at = &t
	}
	db, ws, err := openDefault(ctx, *dsn)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	items, err := versions.Inventory(ctx, db, ws.Scope(), at)
	if err != nil {
		return err
	}
	if *asCSV {
		cw := csv.NewWriter(out)
		_ = cw.Write([]string{"environment", "target", "platform", "service", "namespace", "workload", "kind", "container", "image", "tag", "digest", "running", "first_seen"})
		for _, i := range items {
			_ = cw.Write([]string{
				i.Environment, i.Target, i.Platform, i.Service, i.Namespace, i.Workload, i.Kind, i.Container,
				i.Image, i.Tag, i.Digest, strconv.Itoa(i.Running), i.FirstSeen.UTC().Format(time.RFC3339),
			})
		}
		cw.Flush()
		return cw.Error()
	}
	tw := tabwriter.NewWriter(out, 0, 0, 3, ' ', 0)
	_, _ = fmt.Fprintln(tw, "ENV\tTARGET\tSERVICE\tWORKLOAD\tCONTAINER\tIMAGE\tRUNNING")
	for _, i := range items {
		svc := i.Service
		if svc == "" {
			svc = "-"
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%d\n", i.Environment, i.Target, svc, i.Workload, i.Container, i.Image, i.Running)
	}
	return tw.Flush()
}

func hygieneCmd(ctx context.Context, args []string, out io.Writer) error {
	fs, dsn := newFlags("hygiene")
	if err := fs.Parse(args); err != nil {
		return err
	}
	versions.SetAllowedRegistries(splitList(os.Getenv("GOLIASH_ALLOWED_REGISTRIES")))
	db, ws, err := openDefault(ctx, *dsn)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	findings, err := versions.LoadHygiene(ctx, db, ws.Scope())
	if err != nil {
		return err
	}
	tw := tabwriter.NewWriter(out, 0, 0, 3, ' ', 0)
	_, _ = fmt.Fprintln(tw, "SEVERITY\tKIND\tIMAGE\tDETAIL\tWHERE")
	for _, f := range findings {
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", f.Severity, f.Kind, f.Image, f.Detail, strings.Join(f.Where, "; "))
	}
	return tw.Flush()
}

// loopbackURL is how the server reaches its own REST API (for the MCP endpoint).
func loopbackURL(listen string) string {
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return "http://127.0.0.1:8080"
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port)
}

func mcpCmd(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("mcp", flag.ContinueOnError)
	server := fs.String("server", envOr("GOLIASH_URL", "http://localhost:8080"), "Goliash server URL (env GOLIASH_URL)")
	token := fs.String("token", os.Getenv("GOLIASH_TOKEN"), "API token, glsh_api_… (env GOLIASH_TOKEN)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *token == "" {
		return errors.New("set GOLIASH_TOKEN (goliash token create -name mcp) or -token")
	}
	return mcpserver.NewServer(&mcpserver.Client{BaseURL: *server, Token: *token}).Run(ctx, &mcp.StdioTransport{})
}

// newLogger logs as text (people) or JSON (log collectors such as Loki or Elasticsearch).
func newLogger(format string, level slog.Level) (*slog.Logger, error) {
	opts := &slog.HandlerOptions{Level: level}
	switch format {
	case "", "text":
		return slog.New(slog.NewTextHandler(os.Stderr, opts)), nil
	case "json":
		return slog.New(slog.NewJSONHandler(os.Stderr, opts)), nil
	}
	return nil, fmt.Errorf("log format %q: use text or json", format)
}

func envInt(key string, fallback int) int {
	if n, err := strconv.Atoi(os.Getenv(key)); err == nil {
		return n
	}
	return fallback
}

func envDuration(key string, fallback time.Duration) time.Duration {
	if d, err := time.ParseDuration(os.Getenv(key)); err == nil {
		return d
	}
	return fallback
}

// backupTo writes goliash-<time>.db into dir and copies the secret key file next to
// it, so channel secrets can be read after a restore. It returns the backup's path.
func backupTo(ctx context.Context, db *store.Store, dsn, dir string, now time.Time) (string, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil { //nolint:gosec // the operator names the backup directory
		return "", err
	}
	path := filepath.Join(dir, "goliash-"+now.UTC().Format("20060102T150405Z")+".db")
	if err := db.Backup(ctx, path); err != nil {
		return "", err
	}
	if os.Getenv("GOLIASH_SECRET_KEY") == "" && os.Getenv("GOLIASH_SECRET_KEY_FILE") == "" {
		key := filepath.Join(filepath.Dir(strings.TrimPrefix(dsn, "sqlite://")), "goliash.key")
		if b, err := os.ReadFile(key); err == nil { //nolint:gosec // next to the operator's database
			if err := os.WriteFile(strings.TrimSuffix(path, ".db")+".key", b, 0o600); err != nil { //nolint:gosec // inside the backup directory
				return path, err
			}
		}
	}
	return path, nil
}

// pruneBackups keeps the newest keep backups in dir.
func pruneBackups(dir string, keep int) error {
	matches, err := filepath.Glob(filepath.Join(dir, "goliash-*.db"))
	if err != nil || len(matches) <= keep {
		return err
	}
	sort.Strings(matches) // names sort by time
	for _, old := range matches[:len(matches)-keep] {
		if err := os.Remove(old); err != nil { //nolint:gosec // a backup this server wrote
			return err
		}
		_ = os.Remove(strings.TrimSuffix(old, ".db") + ".key") //nolint:gosec // its key, beside it
	}
	return nil
}

// backups writes a SQLite backup at start and every day after, keeping keep of them.
func backups(ctx context.Context, db *store.Store, dsn, dir string, keep int, log *slog.Logger) {
	if db.Dialect() != store.SQLite {
		log.Warn("GOLIASH_BACKUP_DIR is for SQLite; back PostgreSQL up with pg_dump")
		return
	}
	ticker := time.NewTicker(24 * time.Hour)
	defer ticker.Stop()
	for {
		if path, err := backupTo(ctx, db, dsn, dir, time.Now()); err != nil && ctx.Err() == nil {
			log.Error("backup failed", "dir", dir, "err", err)
		} else if err == nil {
			log.Info("backup written", "path", path)
			if err := pruneBackups(dir, keep); err != nil {
				log.Error("removing old backups failed", "err", err)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func backupCmd(ctx context.Context, args []string, out io.Writer) error {
	fs, dsn := newFlags("backup")
	dir := fs.String("out", ".", "directory for the backup")
	if err := fs.Parse(args); err != nil {
		return err
	}
	db, _, err := openDefault(ctx, *dsn)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	path, err := backupTo(ctx, db, *dsn, *dir, time.Now())
	if errors.Is(err, store.ErrBackupUnsupported) {
		return errors.New("PostgreSQL: use pg_dump, e.g. pg_dump -Fc \"$GOLIASH_DATABASE_URL\" > goliash.dump")
	}
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintf(out, "backup written to %s\n", path)
	return nil
}
