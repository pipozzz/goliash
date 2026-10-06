// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package ui

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/pipozzz/goliash/internal/auth"
	"github.com/pipozzz/goliash/internal/notifier"
	"github.com/pipozzz/goliash/internal/store"
)

// deliveryView says how a queued notification went: sent, waiting for its digest,
// failing and retried, or given up.
func deliveryView(d store.Delivery, now time.Time) DeliveryView {
	v := DeliveryView{ID: d.ID, At: d.CreatedAt, Channel: d.ChannelName, Type: d.ChannelType, Error: d.LastError}
	var it notifier.Item
	if json.Unmarshal(d.Payload, &it) == nil {
		v.Text = it.Text
	}
	switch {
	case d.SentAt != nil:
		v.At, v.Status, v.Label = *d.SentAt, "sent", "sent"
		if d.Attempts > 1 {
			v.Label = "sent on attempt " + itoa(d.Attempts)
		}
	case d.Attempts >= notifier.MaxAttempts:
		v.Status, v.Label, v.Retry = "failed", "gave up after "+itoa(d.Attempts)+" attempts", true
	case d.Attempts > 0:
		v.Status, v.Label, v.Retry = "retrying", "attempt "+itoa(d.Attempts)+" failed; next "+until(d.DueAt, now), true
	case d.DueAt.After(now.Add(time.Minute)):
		v.Status, v.Label, v.Retry = "waiting", "in the digest "+until(d.DueAt, now), true
	default:
		v.Status, v.Label = "waiting", "going out now"
	}
	return v
}

// until says when t comes, roughly: "in 5 min", "in 3 h", "on Mon 09:00 UTC".
func until(t, now time.Time) string {
	d := t.Sub(now)
	switch {
	case d < time.Minute:
		return "within a minute"
	case d < time.Hour:
		return "in " + itoa(int(d.Minutes())) + " min"
	case d < 24*time.Hour:
		return "in " + itoa(int(d.Hours())) + " h"
	}
	return "on " + t.UTC().Format("Mon 15:04") + " UTC"
}

// retryDelivery sends a failed or waiting notification now.
func (s *Server) retryDelivery(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	ctx := r.Context()
	ok, err := s.store.RetryDelivery(ctx, p.Scope, r.PathValue("id"))
	if err != nil {
		return err
	}
	if !ok {
		return back(w, r, "/notifications", "notice", "That notification was already sent.")
	}
	s.audit(ctx, p, "notification.retry", "delivery", r.PathValue("id"))
	if err := s.notify.DeliverDue(ctx); err != nil {
		return back(w, r, "/notifications", "error", "Sending failed: "+err.Error())
	}
	ds, err := s.store.RecentDeliveries(ctx, p.Scope, 100)
	if err != nil {
		return err
	}
	for _, d := range ds {
		if d.ID != r.PathValue("id") {
			continue
		}
		if d.SentAt != nil {
			return back(w, r, "/notifications", "notice", "Sent to "+d.ChannelName+".")
		}
		return back(w, r, "/notifications", "error", "Sending to "+d.ChannelName+" failed again: "+d.LastError)
	}
	return back(w, r, "/notifications", "notice", "Sent.")
}
