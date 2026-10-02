// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: Apache-2.0

package buildinfo

import "testing"

func TestString(t *testing.T) {
	Version, Commit, Date = "v0.1.0", "abc123", "2026-10-02"
	t.Cleanup(func() { Version, Commit, Date = "dev", "unknown", "unknown" })

	want := "v0.1.0 (commit abc123, built 2026-10-02)"
	if got := String(); got != want {
		t.Fatalf("String() = %q, want %q", got, want)
	}
}
