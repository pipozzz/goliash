// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"context"
	"io"
	"strings"
	"testing"
)

// Flags before the command must not silently start a server.
func TestFlagsBeforeCommand(t *testing.T) {
	err := run(context.Background(), []string{"-database", t.TempDir() + "/x.db", "matrix"}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "goliash matrix -database") {
		t.Fatalf("err = %v", err)
	}
}
