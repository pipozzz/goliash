// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package ui

import (
	"context"
	"sync"
	"time"

	"github.com/pipozzz/goliash/internal/gitops"
	"github.com/pipozzz/goliash/internal/versions"
)

// pinsTTL is how long what a repository pins is trusted before it is read again.
const pinsTTL = 10 * time.Minute

// pinsEntry is what an application's files pinned when they were last read.
type pinsEntry struct {
	tags    []string
	at      time.Time
	reading bool
}

type pinsCache struct {
	mu   sync.Mutex
	byID map[string]pinsEntry
}

var pins = pinsCache{byID: map[string]pinsEntry{}}

// gitPins is the version Git pins for an application that is out of sync, when it differs from the version that
// runs: why a change did not go out. It answers from what was last read and never waits on the forge: a stale or
// missing entry is read in the background, for the next view. "" when unknown, the same or ambiguous.
func (s *Server) gitPins(a versions.GitOpsApp, now time.Time) string {
	if a.Trouble(now) != "out of sync" || !s.canPR(a) {
		return ""
	}
	b := gitops.Bump{Repo: versions.RepoWebURL(a.Repo), Branch: a.TargetRevision, Path: a.Path, Image: versions.ParseImage(a.Image).Repo()}
	key := b.Repo + "|" + b.Branch + "|" + b.Path + "|" + b.Image
	pins.mu.Lock()
	e, ok := pins.byID[key]
	if (!ok || now.Sub(e.at) > pinsTTL) && !e.reading {
		e.reading = true
		pins.byID[key] = e
		go s.readPins(key, b)
	}
	pins.mu.Unlock()
	if len(e.tags) != 1 || e.tags[0] == a.Tag {
		return ""
	}
	return e.tags[0]
}

// readPins reads what the application's files pin, and keeps it, failures included (as nothing), until pinsTTL.
func (s *Server) readPins(key string, b gitops.Bump) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	tags, err := s.pulls.Pinned(ctx, b)
	if err != nil {
		s.log.Debug("reading what Git pins failed", "repo", b.Repo, "path", b.Path, "err", err)
		tags = nil
	}
	pins.mu.Lock()
	pins.byID[key] = pinsEntry{tags: tags, at: time.Now()}
	pins.mu.Unlock()
}
