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
	flag.Parse()

	if *showVersion {
		fmt.Println("goliash-agent", buildinfo.String())
		return
	}

	level := slog.LevelInfo
	if *debug {
		level = slog.LevelDebug
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))

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
		return t, nil
	}
	path := os.Getenv("GOLIASH_AGENT_TOKEN_FILE")
	if path == "" {
		return "", errors.New("set GOLIASH_AGENT_TOKEN or GOLIASH_AGENT_TOKEN_FILE")
	}
	b, err := os.ReadFile(path) //nolint:gosec // path comes from the operator
	if err != nil {
		return "", fmt.Errorf("agent token file: %w", err)
	}
	return strings.TrimSpace(string(b)), nil
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
