// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: Apache-2.0

// Package buildinfo exposes version metadata injected at build time via -ldflags.
package buildinfo

import "fmt"

// Set via -ldflags "-X github.com/pipozzz/goliash/pkg/buildinfo.Version=...".
var (
	Version = "dev"
	Commit  = "unknown"
	Date    = "unknown"
)

// String returns a one-line description of the build.
func String() string {
	return fmt.Sprintf("%s (commit %s, built %s)", Version, Commit, Date)
}
