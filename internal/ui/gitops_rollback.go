package ui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/pipozzz/goliash/internal/store"
	"github.com/pipozzz/goliash/internal/versions"
)

// rollbackWindow is how long after a version change a degraded application is offered a rollback to the version
// before it.
const rollbackWindow = 7 * 24 * time.Hour

// rollbackFor is the version a rollback pull request for the application would pin, and why: when the application
// is degraded or missing, and the version it runs replaced another on its cluster in the last week, the version it
// replaced. Else "".
func (s *Server) rollbackFor(ctx context.Context, sc store.Scope, a versions.GitOpsApp, now time.Time) (string, string) {
	trouble := a.Trouble(now)
	if !s.canPR(a) || trouble != "degraded" && trouble != "missing" {
		return "", ""
	}
	from, at, ok := versions.Replaced(ctx, s.store, sc, a, now.Add(-rollbackWindow))
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
