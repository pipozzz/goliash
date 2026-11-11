// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: Apache-2.0

package memlimit

import "testing"

func TestMemoryLimit(t *testing.T) {
	const mib = 1 << 20
	for _, c := range []struct{ cgroup, machine, want int64 }{
		{512 * mib, 8192 * mib, 512 * mib * 2 / 3}, // a container limit: Goliash's own
		{0, 909 * mib, 227 * mib},                  // a small shared machine: a quarter (909/4 rounds down)
		{0, 512 * mib, 192 * mib},                  // not below 192 MiB
		{0, 256 * mib, 256 * mib * 2 / 3},          // nor above two thirds
		{0, 0, 0},
	} {
		if got, _ := memoryLimit(c.cgroup, c.machine); got/mib != c.want/mib {
			t.Errorf("cgroup %d, machine %d: %d MiB, want %d", c.cgroup/mib, c.machine/mib, got/mib, c.want/mib)
		}
	}
}
