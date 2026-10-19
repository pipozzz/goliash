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
	evs, err := s.store.ListEvents(ctx, sc, store.EventFilter{
		ServiceID: a.ServiceID, Types: []string{"version_changed"}, Since: now.Add(-rollbackWindow),
	})
	if err != nil {
		return "", ""
	}
	running := strings.TrimPrefix(a.Tag, "v")
	for _, e := range evs { // newest first
		if e.TargetID != a.TargetID || strings.TrimPrefix(e.ToVersion, "v") != running {
			continue
		}
		from := e.FromVersion
		if from == "" || strings.TrimPrefix(from, "v") == running {
			return "", ""
		}
		if strings.HasPrefix(a.Tag, "v") != strings.HasPrefix(from, "v") { // pin it the way the repository does
			from = strings.TrimPrefix(from, "v")
			if strings.HasPrefix(a.Tag, "v") {
				from = "v" + from
			}
		}
		return from, fmt.Sprintf("%s has been %s since %s, after it replaced %s on %s on %s",
			a.App, trouble, a.SyncSince.UTC().Format("2 Jan 15:04 MST"), from, a.Target, e.At.UTC().Format("2 Jan 15:04 MST"))
	}
	return "", ""
}
