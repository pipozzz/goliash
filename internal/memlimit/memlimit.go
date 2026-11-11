// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: Apache-2.0

// Package memlimit sets Go's soft memory limit from the container's or the machine's memory, for the server and
// the agent alike.
package memlimit

import (
	"bufio"
	"log/slog"
	"os"
	"runtime/debug"
	"strconv"
	"strings"
)

// Set sets a soft memory limit when GOMEMLIMIT is not set. Near it the garbage collector works harder
// instead of letting the heap grow into swap, which on a small host stalls everything and reads the disk for every
// process. With a container memory limit (cgroup) Goliash has that memory to itself: two thirds of it. Without one
// it shares the machine with whatever else runs there: a quarter of the machine's memory, at least 192 MiB.
func Set(log *slog.Logger) {
	if os.Getenv("GOMEMLIMIT") != "" {
		return
	}
	limit, total := memoryLimit(cgroupLimit(), machineMemory())
	if limit <= 0 {
		return
	}
	debug.SetMemoryLimit(limit)
	log.Info("memory limit", "soft_limit_mb", limit>>20, "of_mb", total>>20, "set_with", "GOMEMLIMIT")
}

// minSharedLimit is the least a shared machine's quarter is raised to: below it the collector would run all the time.
const minSharedLimit = 192 << 20

// memoryLimit is the soft limit for a container limit (0: none) and the machine's memory, and what it is a part of.
func memoryLimit(cgroup, machine int64) (limit, of int64) {
	switch {
	case cgroup > 0:
		return cgroup * 2 / 3, cgroup
	case machine > 0:
		return min(max(machine/4, minSharedLimit), machine*2/3), machine
	}
	return 0, 0
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
