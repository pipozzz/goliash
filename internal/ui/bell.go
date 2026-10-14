// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package ui

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"time"

	"github.com/pipozzz/goliash/internal/auth"
	"github.com/pipozzz/goliash/internal/notifier"
	"github.com/pipozzz/goliash/internal/store"
)

// The bell: the notifications Goliash sent in the workspace, through any channel, each message once, with what
// the person has not seen yet. Messages are kept for 30 days.

const (
	bellKept   = 30 * 24 * time.Hour
	bellListed = 50
	bellLines  = 4
	bellMax    = 99 // the count stops here
)

// bellUnread counts the messages since the person last opened the bell.
func (s *Server) bellUnread(ctx context.Context, sc store.Scope, userID string) int {
	if userID == "" {
		return 0
	}
	seen, err := s.store.NotificationsSeen(ctx, sc, userID)
	if err != nil {
		return 0
	}
	if since := time.Now().Add(-bellKept); seen.Before(since) {
		seen = since
	}
	feed, err := s.store.NotificationFeed(ctx, sc, seen, bellMax)
	if err != nil {
		return 0
	}
	return len(feed)
}

type bellItem struct {
	At     time.Time `json:"at"`
	Title  string    `json:"title"`
	Lines  []string  `json:"lines"`
	More   int       `json:"more,omitempty"`
	URL    string    `json:"url"`
	Kind   string    `json:"kind"` // the first item's type, for its mark
	Unread bool      `json:"unread"`
	Ask    string    `json:"ask,omitempty"` // a question for Goli about it, when Goli is on
}

// bellList answers the bell: the latest messages, which are unread, and the count.
func (s *Server) bellList(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	ctx := r.Context()
	seen, err := s.store.NotificationsSeen(ctx, p.Scope, p.User.ID)
	if err != nil {
		return err
	}
	feed, err := s.store.NotificationFeed(ctx, p.Scope, time.Now().Add(-bellKept), bellListed)
	if err != nil {
		return err
	}
	out := struct {
		Unread int        `json:"unread"`
		Items  []bellItem `json:"items"`
	}{Items: []bellItem{}}
	for _, m := range feed {
		msg := notifier.Message{Workspace: p.Workspace.Name, Digest: m.Mode != "instant" || len(m.Items) > 1}
		for _, raw := range m.Items {
			var it notifier.Item
			if json.Unmarshal(raw, &it) == nil {
				msg.Items = append(msg.Items, it)
			}
		}
		if len(msg.Items) == 0 {
			continue
		}
		b := bellItem{At: m.SentAt, Title: msg.Title(), URL: bellLink(msg), Kind: msg.Items[0].Type, Unread: m.SentAt.After(seen)}
		if s.goliOn() {
			b.Ask = notifier.GoliQuestion(msg)
		}
		if b.Unread {
			out.Unread++
		}
		if msg.Digest || len(msg.Items) > 1 {
			for i, it := range msg.Items {
				if i == bellLines {
					b.More = len(msg.Items) - bellLines
					break
				}
				b.Lines = append(b.Lines, it.Text)
			}
		}
		out.Items = append(out.Items, b)
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	return json.NewEncoder(w).Encode(out)
}

// bellLink is the page a message is about: Updates for an upgrade plan, the service of a single alert, else
// History.
func bellLink(msg notifier.Message) string {
	plan := true
	for _, it := range msg.Items {
		if it.Type != "update" {
			plan = false
		}
	}
	switch {
	case plan:
		return "/updates"
	case len(msg.Items) == 1 && msg.Items[0].Type == "posture":
		return "/security"
	case len(msg.Items) == 1 && msg.Items[0].Service != "":
		return "/services/" + url.PathEscape(msg.Items[0].Service)
	}
	return "/events"
}

// bellCount is the unread count alone, for the badge and the installed app's icon.
func (s *Server) bellCount(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	return json.NewEncoder(w).Encode(map[string]int{"unread": s.bellUnread(r.Context(), p.Scope, p.User.ID)})
}

// bellSeen records that the person opened the bell.
func (s *Server) bellSeen(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	if err := s.store.SeeNotifications(r.Context(), p.Scope, p.User.ID); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}
