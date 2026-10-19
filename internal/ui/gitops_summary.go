// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package ui

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/pipozzz/goliash/internal/store"
	"github.com/pipozzz/goliash/internal/versions"
)

// summaryInstructions ask Goli for what a reviewer of an upgrade pull request needs from the release notes.
const summaryInstructions = `You read the release notes between two versions of a piece of software for someone reviewing a pull request that upgrades it.
Answer in markdown, as a list of at most 6 lines that start with "- ", nothing before or after the list.
First what breaks or needs a migration or a configuration change (say "Nothing breaking in the release notes." when there is none), then the notable fixes, security fixes first.
Be concrete and short: name the setting, flag or API. Do not invent anything that is not in the notes.`

// summaryCache keeps Goli's summaries of release notes, so a preview opened twice asks once.
type summaryCache struct {
	mu   sync.Mutex
	byID map[string]summaryEntry
}

type summaryEntry struct {
	text string
	at   time.Time
}

var summaries = summaryCache{byID: map[string]summaryEntry{}}

// addSummary asks Goli, when it is on, what the release notes between the running version and the new one say a
// reviewer should know, for the pull request's description. Without Goli, release notes or an answer, nothing.
func (s *Server) addSummary(ctx context.Context, sc store.Scope, u *upgrade) {
	if !s.goliOn() || s.checker == nil || u.rollback { // a rollback's case is the trouble, not the release notes
		return
	}
	key := u.app.ServiceID + "|" + u.app.Tag + "|" + u.to
	summaries.mu.Lock()
	e, ok := summaries.byID[key]
	summaries.mu.Unlock()
	if ok && time.Since(e.at) < 24*time.Hour {
		u.summary = e.text
		return
	}
	svc, err := s.store.GetService(ctx, sc, u.app.ServiceID)
	if err != nil {
		return
	}
	notes, err := s.checker.ReleaseNotes(ctx, svc, versions.ParseImage(u.app.Image).Repo(), u.app.Tag, u.to)
	if err != nil || len(notes.Releases) == 0 {
		return
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s, from %s to %s.\n\n", svc.Name, u.app.Tag, u.to)
	for _, r := range notes.Releases {
		fmt.Fprintf(&b, "## %s\n\n%s\n\n", r.Version, r.Notes)
	}
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	text, err := s.goli.Complete(ctx, summaryInstructions, b.String())
	if err != nil {
		s.log.WarnContext(ctx, "Goli could not summarise release notes", "service", svc.Name, "err", err)
		return
	}
	var lines []string
	for _, l := range strings.Split(text, "\n") {
		if l = strings.TrimSpace(l); strings.HasPrefix(l, "- ") {
			lines = append(lines, l)
		}
	}
	if len(lines) == 0 {
		return
	}
	u.summary = strings.Join(lines, "\n")
	summaries.mu.Lock()
	summaries.byID[key] = summaryEntry{u.summary, time.Now()}
	summaries.mu.Unlock()
}
