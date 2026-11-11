// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"log/slog"
	"net/http"
	"net/http/pprof"
	"os"
	"time"
)

// servePprof serves Go's profiles on GOLIASH_PPROF (an address such as 127.0.0.1:6060), for finding what is slow
// or large on a running server. Off unless set; keep it on localhost.
func servePprof(log *slog.Logger) {
	addr := os.Getenv("GOLIASH_PPROF")
	if addr == "" {
		return
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
	srv := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		log.Info("profiles served", "address", "http://"+addr+"/debug/pprof/")
		if err := srv.ListenAndServe(); err != nil {
			log.Error("profiles not served", "err", err)
		}
	}()
}
