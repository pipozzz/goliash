// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package ui

import (
	"time"

	"github.com/pipozzz/goliash/internal/store"
)

// syncWindowText says that Argo CD's sync windows keep the application from syncing at now, and until when; ""
// when they do not.
func syncWindowText(a store.WorkloadGitOps, now time.Time) string {
	if !a.Blocked(now) {
		return ""
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
		return "> ⏸️ " + s + ": merged, the change deploys when the window allows it.\n\n"
	}
	return ""
}
