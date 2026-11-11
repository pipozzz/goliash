// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"bufio"
	"log/slog"
	"net/http"
	"net/http/pprof"
	"os"
	"runtime/debug"
	"strconv"
	"strings"
	"time"
)

// limitMemory sets a soft memory limit when GOMEMLIMIT is not set: two thirds of the container's memory limit
// (cgroup), else of the machine's memory. Near it the garbage collector works harder instead of letting the heap
// grow into swap, which on a small host stalls everything.
func limitMemory(log *slog.Logger) {
	if os.Getenv("GOMEMLIMIT") != "" {
		return
	}
	total := cgroupLimit()
	if total <= 0 {
		total = machineMemory()
	}
	if total <= 0 {
		return
	}
	limit := total * 2 / 3
	debug.SetMemoryLimit(limit)
	log.Info("memory limit", "soft_limit_mb", limit>>20, "of_mb", total>>20, "set_with", "GOMEMLIMIT")
}

// cgroupLimit is the container's memory limit in bytes, 0 when there is none.
func cgroupLimit() int64 {
	for _, f := range []string{"/sys/fs/cgroup/memory.max", "/sys/fs/cgroup/memory/memory.limit_in_bytes"} {
		b, err := os.ReadFile(f) //nolint:gosec // one of two fixed cgroup paths
		if err != nil {
			continue
		}
		v := strings.TrimSpace(string(b))
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 && n < 1<<50 { // "max", or a huge number: none
			return n
		}
	}
	return 0
}

// machineMemory is the machine's memory in bytes, from /proc/meminfo; 0 elsewhere.
func machineMemory() int64 {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if v, ok := strings.CutPrefix(sc.Text(), "MemTotal:"); ok {
			kb, err := strconv.ParseInt(strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(v), "kB")), 10, 64)
			if err == nil {
				return kb << 10
			}
		}
	}
	return 0
}

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
