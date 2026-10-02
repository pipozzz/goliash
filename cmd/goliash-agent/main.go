// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: Apache-2.0

// Command goliash-agent runs inside the customer network, reads orchestrators
// read-only and sends snapshots to a Goliash server over outbound HTTPS.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/pipozzz/goliash/pkg/buildinfo"
)

func main() {
	server := flag.String("server", os.Getenv("GOLIASH_SERVER_URL"), "Goliash server URL (env GOLIASH_SERVER_URL)")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Println("goliash-agent", buildinfo.String())
		return
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	slog.Info("goliash-agent starting", "server", *server, "version", buildinfo.Version)
	// The agent loop (register, config, snapshot, heartbeat) lands in internal/agent.
	<-ctx.Done()
	slog.Info("goliash-agent stopped")
}
