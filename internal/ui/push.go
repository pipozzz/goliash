// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package ui

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/pipozzz/goliash/internal/auth"
	"github.com/pipozzz/goliash/internal/notifier"
	"github.com/pipozzz/goliash/internal/store"
	"github.com/pipozzz/goliash/internal/webpush"
)

// Web push: a person adds their browser to a push channel from the Notifications
// page. app.js registers /sw.js, subscribes with the server's VAPID key and posts
// the subscription here; rules then send to every browser on the channel.

// serviceWorker serves /sw.js from the root, so its scope is the whole site.
func (s *Server) serviceWorker(w http.ResponseWriter, _ *http.Request) {
	b, err := static.ReadFile("static/sw.js")
	if err != nil {
		http.NotFound(w, nil)
		return
	}
	w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Service-Worker-Allowed", "/")
	_, _ = w.Write(b)
}

// manifest makes Goliash installable: a home screen icon, its own window, and on iOS
// the web push that only installed web apps get.
func (s *Server) manifest(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/manifest+json")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"name": "Goliash", "short_name": "Goliash", "description": "What runs where, and what is behind.",
		"start_url": "/", "scope": "/", "display": "standalone",
		"background_color": "#1b2a6b", "theme_color": "#1b2a6b",
		"icons": []map[string]string{
			{"src": "/static/icon-192.png", "sizes": "192x192", "type": "image/png"},
			{"src": "/static/icon-512.png", "sizes": "512x512", "type": "image/png", "purpose": "any maskable"},
		},
	})
}

// pushChannel returns the workspace's push channel named by the path.
func (s *Server) pushChannel(r *http.Request, p auth.Principal) (store.Channel, error) {
	chans, err := s.store.ListChannels(r.Context(), p.Scope)
	if err != nil {
		return store.Channel{}, err
	}
	for _, c := range chans {
		if c.ID == r.PathValue("id") && c.Type == "push" {
			return c, nil
		}
	}
	return store.Channel{}, store.ErrNotFound
}

func readJSON(r *http.Request, v any) error {
	return json.NewDecoder(io.LimitReader(r.Body, 8<<10)).Decode(v)
}

func writeJSON(w http.ResponseWriter, code int, v any) error {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	return json.NewEncoder(w).Encode(v)
}

// pushSubscribe adds the person's browser to a push channel.
func (s *Server) pushSubscribe(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	ch, err := s.pushChannel(r, p)
	if err != nil {
		return writeJSON(w, http.StatusNotFound, map[string]string{"error": "That channel is gone."})
	}
	var sub webpush.Subscription
	if err := readJSON(r, &sub); err != nil {
		return writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Not a push subscription."})
	}
	if err := sub.Check(); err != nil {
		return writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
	}
	keys, _ := json.Marshal(sub.Keys)
	if _, err := s.store.SavePushSubscription(r.Context(), p.Scope, store.PushSubscription{
		ChannelID: ch.ID, UserID: p.User.ID, Endpoint: sub.Endpoint, Keys: string(keys), Label: browserName(r.UserAgent()),
	}); err != nil {
		return err
	}
	s.audit(r.Context(), p, "push.subscribe", "channel", ch.Name, "browser", browserName(r.UserAgent()))
	return writeJSON(w, http.StatusOK, map[string]string{"notice": "This browser now gets the notifications of " + ch.Name + "."})
}

// pushUnsubscribe removes the person's browser from a push channel.
func (s *Server) pushUnsubscribe(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	ch, err := s.pushChannel(r, p)
	if err != nil {
		return writeJSON(w, http.StatusNotFound, map[string]string{"error": "That channel is gone."})
	}
	var body struct {
		Endpoint string `json:"endpoint"`
	}
	if err := readJSON(r, &body); err != nil || body.Endpoint == "" {
		return writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Which browser?"})
	}
	if _, err := s.store.DeletePushSubscription(r.Context(), p.Scope, ch.ID, body.Endpoint, p.User.ID); err != nil {
		return err
	}
	s.audit(r.Context(), p, "push.unsubscribe", "channel", ch.Name)
	return writeJSON(w, http.StatusOK, map[string]string{"notice": "This browser no longer gets the notifications of " + ch.Name + "."})
}

// pushStatus tells app.js which push channels this browser (its endpoint) is on.
func (s *Server) pushStatus(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	var body struct {
		Endpoint string `json:"endpoint"`
	}
	if err := readJSON(r, &body); err != nil {
		return writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Which browser?"})
	}
	chans, err := s.store.ListChannels(r.Context(), p.Scope)
	if err != nil {
		return err
	}
	on := []string{}
	for _, c := range chans {
		if c.Type != "push" {
			continue
		}
		subs, err := s.store.ListPushSubscriptions(r.Context(), p.Scope, c.ID)
		if err != nil && !errors.Is(err, store.ErrNoSecretKey) {
			return err
		}
		for _, sub := range subs {
			if sub.Endpoint == body.Endpoint && sub.UserID == p.User.ID {
				on = append(on, c.ID)
				break
			}
		}
	}
	return writeJSON(w, http.StatusOK, map[string]any{"channels": on})
}

// pushKey is the server's VAPID public key, which browsers subscribe with.
func (s *Server) pushKey(r *http.Request) string {
	k, err := notifier.PushKeys(r.Context(), s.store)
	if err != nil {
		s.log.Warn("web push keys", "err", err)
		return ""
	}
	return k.Public
}

// browserName names a browser from its User-Agent, for the list of subscriptions.
func browserName(ua string) string {
	name := "A browser"
	switch {
	case strings.Contains(ua, "Edg/"):
		name = "Edge"
	case strings.Contains(ua, "Firefox/"):
		name = "Firefox"
	case strings.Contains(ua, "Chrome/"):
		name = "Chrome"
	case strings.Contains(ua, "Safari/"):
		name = "Safari"
	}
	switch {
	case strings.Contains(ua, "iPhone"), strings.Contains(ua, "iPad"):
		name += " on iOS"
	case strings.Contains(ua, "Android"):
		name += " on Android"
	case strings.Contains(ua, "Mac OS X"):
		name += " on macOS"
	case strings.Contains(ua, "Windows"):
		name += " on Windows"
	case strings.Contains(ua, "Linux"):
		name += " on Linux"
	}
	return name
}
