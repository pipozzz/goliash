// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package ui

import (
	"time"

	"github.com/pipozzz/goliash/internal/store"
	"github.com/pipozzz/goliash/internal/versions"
)

// syncWindowText says that the application does not sync at now, and until when: Argo CD's sync windows keep it
// from syncing, or Flux has it suspended; "" when neither.
func syncWindowText(a store.WorkloadGitOps, now time.Time) string {
	if !a.Blocked(now) {
		return ""
	}
	if a.Tool == "flux" {
		return a.App + " is " + a.SyncBlocked
	}
	s := "Argo CD's sync windows keep " + a.App + " from syncing (" + a.SyncBlocked + ")"
	if !a.SyncBlockedUntil.IsZero() {
		s += " until " + a.SyncBlockedUntil.Format("Mon 2 Jan 15:04 MST")
	}
	return s
}

// syncWindowNote is what a pull request for an application a sync window holds says first; "" for one it
// does not.
func syncWindowNote(a store.WorkloadGitOps, now time.Time) string {
	if s := syncWindowText(a, now); s != "" {
		if a.Tool == "flux" {
			return "> ⏸️ " + s + ": merged, the change deploys once it is resumed.\n\n"
		}
		return "> ⏸️ " + s + ": merged, the change deploys when the window allows it.\n\n"
	}
	return ""
}

// manualSyncNote is what a pull request for an application Argo CD syncs only by hand says first: merging is not
// deploying. "" for one that syncs by itself.
func (s *Server) manualSyncNote(a versions.GitOpsApp) string {
	if !a.ManualSync {
		return ""
	}
	where := "in Argo CD"
	if u := argoAppURL(a, s.argoCDURL); u != "" {
		where = "in [Argo CD](" + u + ")"
	}
	return "> 🖐️ Argo CD syncs " + a.App + " by hand (no automated sync policy): once merged, sync it " + where + ".\n\n"
}
