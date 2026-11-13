// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package ui

import (
	"os"
	"testing"
)

// The tests' servers listen on loopback, which the guard on typed-in addresses refuses (internal/netguard).
func TestMain(m *testing.M) {
	_ = os.Setenv("GOLIASH_PRIVATE_NETWORKS", "all")
	os.Exit(m.Run())
}
