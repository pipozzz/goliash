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

// othersTTL is how long a repository's list of open pull requests is trusted before it is read again.
const othersTTL = 10 * time.Minute

type othersEntry struct {
	pulls   []gitops.Pull
	at      time.Time
	reading bool
}

var others = struct {
	mu     sync.Mutex
	byRepo map[string]othersEntry
}{byRepo: map[string]othersEntry{}}

// othersPull is a pull request someone else (Renovate, Dependabot, a person) opened to move the application to the
// version, so Goliash links it instead of opening another. It never waits on the forge: a stale or missing list is
// read in the background, for the next view.
func (s *Server) othersPull(a versions.GitOpsApp, to string) (gitops.Pull, bool) {
	if s.pulls == nil || to == "" || !s.canPR(a) {
		return gitops.Pull{}, false
	}
	repo := versions.RepoWebURL(a.Repo)
	others.mu.Lock()
	e, ok := others.byRepo[repo]
	if (!ok || time.Since(e.at) > othersTTL) && !e.reading {
		e.reading = true
		others.byRepo[repo] = e
		go s.readOthers(context.Background(), repo)
	}
	others.mu.Unlock()
	return gitops.Proposing(e.pulls, versions.ParseImage(a.Image).Repo(), to)
}

// othersPullNow is othersPull for what may wait (opening a pull request): it reads the list when it is stale.
func (s *Server) othersPullNow(ctx context.Context, a versions.GitOpsApp, to string) (gitops.Pull, bool) {
	if s.pulls == nil || to == "" || !s.canPR(a) {
		return gitops.Pull{}, false
	}
	repo := versions.RepoWebURL(a.Repo)
	others.mu.Lock()
	e, ok := others.byRepo[repo]
	others.mu.Unlock()
	if !ok || time.Since(e.at) > othersTTL {
		e.pulls = s.readOthers(ctx, repo)
	}
	return gitops.Proposing(e.pulls, versions.ParseImage(a.Image).Repo(), to)
}

// readOthers reads a repository's open pull requests and keeps them, failures included (as none), for othersTTL.
func (s *Server) readOthers(ctx context.Context, repo string) []gitops.Pull {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	pulls, err := s.pulls.OpenPulls(ctx, repo)
	if err != nil {
		s.log.Debug("listing open pull requests failed", "repo", repo, "err", err)
		pulls = nil
	}
	others.mu.Lock()
	others.byRepo[repo] = othersEntry{pulls: pulls, at: time.Now()}
	others.mu.Unlock()
	return pulls
}

// othersTitle explains a link to a pull request someone else opened: its title. "" for Goliash's own.
func othersTitle(title string) string {
	if title == "" {
		return ""
	}
	return "Opened outside Goliash: " + title
}
