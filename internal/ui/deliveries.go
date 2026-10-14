// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package ui

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
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
	case d.DueAt.After(now.Add(time.Minute)) && d.RuleMode == "instant":
		v.Status, v.Label, v.Retry = "waiting", "held for quiet hours, out "+until(d.DueAt, now), true
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
		return back(w, r, notificationsReturn(r), "notice", "That notification was already sent.")
	}
	s.audit(ctx, p, "notification.retry", "delivery", r.PathValue("id"))
	if err := s.notify.DeliverDue(ctx); err != nil {
		return back(w, r, notificationsReturn(r), "error", "Sending failed: "+err.Error())
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
			return back(w, r, notificationsReturn(r), "notice", "Sent to "+d.ChannelName+".")
		}
		return back(w, r, notificationsReturn(r), "error", "Sending to "+d.ChannelName+" failed again: "+d.LastError)
	}
	return back(w, r, notificationsReturn(r), "notice", "Sent.")
}

// SentView is every notification of the last 30 days, filtered.
type SentView struct {
	Base
	Deliveries []DeliveryView
	Hidden     int // matching notifications not shown
	Status     string
	Channel    string
	Query      string
	Channels   []ChannelView
	Counts     map[string]int // per status, before filtering by status
	Return     string         // this page with its filter, for retries to come back to
}

const sentShown = 300

// sentNotifications lists every notification of the last 30 days (they are kept that long), filtered by status,
// channel and text.
func (s *Server) sentNotifications(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	ctx := r.Context()
	q := r.URL.Query()
	v := SentView{
		Base: withFlash(s.base(ctx, p, "notifications", "Delivery log"), r), Status: q.Get("status"),
		Channel: q.Get("channel"), Query: strings.TrimSpace(q.Get("q")), Counts: map[string]int{},
	}
	switch v.Status {
	case "sent", "waiting", "retrying", "failed":
	default:
		v.Status = "all"
	}
	chans, err := s.store.ListChannels(ctx, p.Scope)
	if err != nil {
		return err
	}
	for _, c := range chans {
		v.Channels = append(v.Channels, ChannelView{ID: c.ID, Name: c.Name, Type: c.Type})
	}
	ds, err := s.store.RecentDeliveries(ctx, p.Scope, 5000)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	needle := strings.ToLower(v.Query)
	for _, d := range ds {
		if v.Channel != "" && d.ChannelID != v.Channel {
			continue
		}
		dv := deliveryView(d, now)
		if needle != "" && !strings.Contains(strings.ToLower(dv.Text+" "+dv.Channel+" "+dv.Error), needle) {
			continue
		}
		v.Counts["all"]++
		v.Counts[dv.Status]++
		if v.Status != "all" && dv.Status != v.Status {
			continue
		}
		if len(v.Deliveries) < sentShown {
			v.Deliveries = append(v.Deliveries, dv)
		} else {
			v.Hidden++
		}
	}
	v.Return = "/notifications/deliveries"
	if r.URL.RawQuery != "" {
		v.Return += "?" + r.URL.RawQuery
	}
	return render(w, r, DeliveryLogPage(v))
}

// notificationsReturn is where a form asks to come back to, when it is a notifications page; else the Notifications page.
func notificationsReturn(r *http.Request) string {
	ret := r.FormValue("return")
	if u, err := url.Parse(ret); err == nil && u.Host == "" && u.Scheme == "" && strings.HasPrefix(u.Path, "/notifications") {
		return ret
	}
	return "/notifications"
}
