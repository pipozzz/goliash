// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package ui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/pipozzz/goliash/internal/store"
	"github.com/pipozzz/goliash/internal/versions"
)

// rollbackFor is the version a rollback pull request for the application would pin, and why: when the application
// is degraded or missing, and the version it runs replaced another on its cluster in the day before that began
// (versions.Cause), the version it replaced. Else "".
func (s *Server) rollbackFor(ctx context.Context, sc store.Scope, a versions.GitOpsApp, now time.Time) (string, string) {
	trouble := a.Trouble(now)
	if !s.canPR(a) || trouble != "degraded" && trouble != "missing" {
		return "", ""
	}
	from, at, ok := versions.Cause(ctx, s.store, sc, a)
	if !ok {
		return "", ""
	}
	if strings.HasPrefix(a.Tag, "v") != strings.HasPrefix(from, "v") { // pin it the way the repository does
		from = strings.TrimPrefix(from, "v")
		if strings.HasPrefix(a.Tag, "v") {
			from = "v" + from
		}
	}
	return from, fmt.Sprintf("%s has been %s since %s, after %s replaced %s on %s on %s",
		a.App, trouble, a.SyncSince.UTC().Format("2 Jan 15:04 MST"), a.Tag, from, a.Target, at.UTC().Format("2 Jan 15:04 MST"))
}
