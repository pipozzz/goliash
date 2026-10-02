// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

// Command goliash is the Goliash server: UI, API and database.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/pipozzz/goliash/internal/store"
	"github.com/pipozzz/goliash/pkg/buildinfo"
)

func main() {
	listen := flag.String("listen", envOr("GOLIASH_LISTEN", ":8080"), "HTTP listen address (env GOLIASH_LISTEN)")
	dsn := flag.String("database", envOr("GOLIASH_DATABASE_URL", "goliash.db"),
		"SQLite file path or postgres:// URL (env GOLIASH_DATABASE_URL)")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Println("goliash", buildinfo.String())
		return
	}

	if err := run(*listen, *dsn); err != nil {
		slog.Error("server stopped", "err", err)
		os.Exit(1)
	}
}

func run(listen, dsn string) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	db, err := store.Open(ctx, dsn)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	ws, err := db.EnsureDefaultWorkspace(ctx)
	if err != nil {
		return fmt.Errorf("default workspace: %w", err)
	}
	slog.Info("database ready", "dialect", db.Dialect(), "workspace", ws.Slug)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		if err := db.Ping(r.Context()); err != nil {
			http.Error(w, "database unavailable", http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte("ok\n"))
	})

	srv := &http.Server{
		Addr:              listen,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		slog.Info("goliash server starting", "listen", listen, "version", buildinfo.Version)
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
	return srv.Shutdown(shutdownCtx)
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
