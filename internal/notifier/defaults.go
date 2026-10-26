// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package notifier

import (
	"context"
	"encoding/json"

	"github.com/pipozzz/goliash/internal/store"
)

// DefaultAlertEvents are what every workspace hears about without setting anything up: a vulnerability exploited
// in the wild or critical in what runs in production, and a release that fixes vulnerabilities in what runs.
var DefaultAlertEvents = []string{"vulnerability", "security_release"}

// defaultChannelName names the bell channel the default alerts go to.
const defaultChannelName = "Goliash bell"

// ensureDefaultAlerts gives a workspace, once, a rule that sends the default alerts to the bell in Goliash: the
// workspace's bell channel, or a new one. A rule deleted or changed afterwards stays as it was left.
func (n *Notifier) ensureDefaultAlerts(ctx context.Context, sc store.Scope) error {
	claimed, err := n.store.ClaimDefaultAlerts(ctx, sc)
	if err != nil || !claimed {
		return err
	}
	channels, err := n.store.ListChannels(ctx, sc)
	if err != nil {
		return err
	}
	var bell store.Channel
	for _, ch := range channels {
		if ch.Type == "inapp" {
			bell = ch
			break
		}
	}
	if bell.ID == "" {
		if bell, err = n.store.CreateChannel(ctx, store.Channel{Scope: sc, Type: "inapp", Name: defaultChannelName, Config: json.RawMessage(`{}`)}); err != nil {
			return err
		}
	}
	filter, err := json.Marshal(Filter{Production: true})
	if err != nil {
		return err
	}
	_, err = n.store.CreateRule(ctx, store.Rule{Scope: sc, ChannelID: bell.ID, EventTypes: DefaultAlertEvents, Filter: filter, Mode: "instant"})
	return err
}

// SecurityAlertsOutside reports whether a rule sends vulnerabilities somewhere outside Goliash (Slack, e-mail…):
// without one, a critical vulnerability waits for someone to open Goliash.
func SecurityAlertsOutside(ctx context.Context, st *store.Store, sc store.Scope) (bool, error) {
	channels, err := st.ListChannels(ctx, sc)
	if err != nil {
		return false, err
	}
	outside := map[string]bool{}
	for _, ch := range channels {
		outside[ch.ID] = ch.Type != "inapp"
	}
	rules, err := st.ActiveRules(ctx, sc)
	if err != nil {
		return false, err
	}
	for _, r := range rules {
		if outside[r.ChannelID] && (len(r.EventTypes) == 0 || contains(r.EventTypes, "vulnerability")) {
			return true, nil
		}
	}
	return false, nil
}

// IsDefaultRule reports whether a rule is the default alerts rule as Goliash added it: the default events in
// production, to an in-app channel. One changed since is the user's own.
func IsDefaultRule(r store.Rule, channelType string) bool {
	if channelType != "inapp" || len(r.EventTypes) != len(DefaultAlertEvents) {
		return false
	}
	for i, e := range DefaultAlertEvents {
		if r.EventTypes[i] != e {
			return false
		}
	}
	var f Filter
	_ = json.Unmarshal(r.Filter, &f)
	return f.Production && len(f.Services)+len(f.Owners)+len(f.Apps)+len(f.Environments) == 0
}
