// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package notifier

import (
	"context"
	"time"
)

// EventGoliashRelease announces a newer release of Goliash itself, once per release: upgrades carry security
// fixes of Goliash and of the Go it is built with, and nobody watches the server's own Status page.
const EventGoliashRelease = "goliash_release"

// selfCheckEvery is how often the notifier looks for a newer Goliash.
const selfCheckEvery = 6 * time.Hour

// SetUpdateCheck makes the notifier announce newer Goliash releases. newer returns the newest release above
// running, or "" (also for a development build).
func (n *Notifier) SetUpdateCheck(running string, newer func(ctx context.Context, running string) (string, error)) {
	n.running, n.newer = running, newer
}

// CheckGoliashRelease queues the announcement of a newer Goliash for every workspace whose rules want it.
func (n *Notifier) CheckGoliashRelease(ctx context.Context) error {
	if n.newer == nil {
		return nil
	}
	look, cancel := context.WithTimeout(ctx, 30*time.Second)
	latest, err := n.newer(look, n.running)
	cancel()
	if err != nil || latest == "" {
		return err
	}
	wss, err := n.store.ListWorkspaces(ctx)
	if err != nil {
		return err
	}
	for _, ws := range wss {
		sc := ws.Scope()
		rules, err := n.store.ActiveRules(ctx, sc)
		if err != nil {
			return err
		}
		it := Item{
			Type: EventGoliashRelease, From: n.running, To: latest, At: n.now(),
			URL: "https://github.com/pipozzz/goliash/releases/tag/v" + latest,
		}
		it.Text = Describe(it)
		if err := n.enqueue(ctx, sc, rules, it, EventGoliashRelease+"|"+latest); err != nil {
			return err
		}
	}
	return nil
}
