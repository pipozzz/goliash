// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: Apache-2.0

// Command goliash-agent runs inside the customer network, reads orchestrators
// read-only and sends snapshots to a Goliash server over outbound HTTPS.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/pipozzz/goliash/internal/agent"
	"github.com/pipozzz/goliash/internal/collectors"
	"github.com/pipozzz/goliash/internal/collectors/compose"
	"github.com/pipozzz/goliash/internal/collectors/docker"
	"github.com/pipozzz/goliash/internal/collectors/ecs"
	"github.com/pipozzz/goliash/internal/collectors/kubernetes"
	"github.com/pipozzz/goliash/internal/collectors/nomad"
	"github.com/pipozzz/goliash/internal/collectors/swarm"
	"github.com/pipozzz/goliash/pkg/agentproto"
	"github.com/pipozzz/goliash/pkg/buildinfo"
)

func main() {
	server := flag.String("server", os.Getenv("GOLIASH_SERVER_URL"), "Goliash server URL (env GOLIASH_SERVER_URL)")
	dataDir := flag.String("data-dir", envOr("GOLIASH_DATA_DIR", "data"), "directory for buffered snapshots (env GOLIASH_DATA_DIR)")
	debug := flag.Bool("debug", os.Getenv("GOLIASH_DEBUG") != "", "debug logging (env GOLIASH_DEBUG)")
	showVersion := flag.Bool("version", false, "print version and exit")
	logFormat := flag.String("log-format", envOr("GOLIASH_LOG_FORMAT", "text"), "text or json (env GOLIASH_LOG_FORMAT)")
	flag.Parse()

	if *showVersion {
		fmt.Println("goliash-agent", buildinfo.String())
		return
	}

	level := slog.LevelInfo
	if *debug {
		level = slog.LevelDebug
	}
	opts := &slog.HandlerOptions{Level: level}
	var log *slog.Logger
	switch *logFormat {
	case "json":
		log = slog.New(slog.NewJSONHandler(os.Stderr, opts))
	case "text", "":
		log = slog.New(slog.NewTextHandler(os.Stderr, opts))
	default:
		fmt.Fprintf(os.Stderr, "log format %q: use text or json\n", *logFormat)
		os.Exit(2)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	token, err := agentToken()
	if err != nil {
		log.Error("invalid configuration", "err", err)
		os.Exit(2)
	}
	a, err := agent.New(agent.Options{
		ServerURL: *server,
		Token:     token,
		DataDir:   *dataDir,
		Collectors: map[agentproto.Platform]collectors.Factory{
			agentproto.Kubernetes: kubernetes.New,
			agentproto.Ecs:        ecs.New,
			agentproto.Nomad:      nomad.New,
			agentproto.Swarm:      swarm.New,
			agentproto.Docker:     docker.New,
			agentproto.Compose:    compose.New,
		},
		Logger: log,
	})
	if err != nil {
		log.Error("invalid configuration", "err", err)
		os.Exit(2)
	}
	log.Info("goliash-agent starting", "version", buildinfo.Version, "server", *server)
	if err := a.Run(ctx); err != nil {
		log.Error("goliash-agent stopped", "err", err)
		os.Exit(1)
	}
	log.Info("goliash-agent stopped")
}

// agentToken reads GOLIASH_AGENT_TOKEN, or the file named by GOLIASH_AGENT_TOKEN_FILE
// (Docker and Kubernetes secrets).
func agentToken() (string, error) {
	if t := os.Getenv("GOLIASH_AGENT_TOKEN"); t != "" {
		return checkToken(cleanToken(t), "GOLIASH_AGENT_TOKEN")
	}
	path := os.Getenv("GOLIASH_AGENT_TOKEN_FILE")
	if path == "" {
		return "", errors.New("set GOLIASH_AGENT_TOKEN or GOLIASH_AGENT_TOKEN_FILE")
	}
	b, err := os.ReadFile(path) //nolint:gosec // path comes from the operator
	if err != nil {
		return "", fmt.Errorf("agent token file: %w", err)
	}
	return checkToken(cleanToken(string(b)), path)
}

// cleanToken drops what copying and configuration files add around a token:
// whitespace, line breaks and quotes.
func cleanToken(t string) string {
	t = strings.TrimSpace(t)
	if len(t) >= 2 && (t[0] == '"' || t[0] == '\'') && t[len(t)-1] == t[0] {
		t = strings.TrimSpace(t[1 : len(t)-1])
	}
	return t
}

// agentTokenLen is the length of glsh_agent_ plus 30 random and 6 checksum characters.
const agentTokenLen = len("glsh_agent_") + 36

// checkToken explains a token that the server would refuse because of its shape,
// without printing it.
func checkToken(t, source string) (string, error) {
	switch {
	case strings.HasPrefix(t, "glsh_api_"), strings.HasPrefix(t, "glsh_ci_"):
		return "", fmt.Errorf("%s holds an API or CI token; the agent needs its own token, glsh_agent_…, shown when the agent is created", source)
	case !strings.HasPrefix(t, "glsh_agent_"):
		return "", fmt.Errorf("%s does not start with glsh_agent_; copy the agent token shown when the agent was created or rotated", source)
	case strings.ContainsAny(t, " \t\r\n\"'"):
		return "", fmt.Errorf("%s contains spaces or quotes inside the token; copy it again", source)
	case len(t) != agentTokenLen:
		return "", fmt.Errorf("%s is %d characters long, an agent token has %d: it was cut or joined when copied", source, len(t), agentTokenLen)
	}
	return t, nil
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
