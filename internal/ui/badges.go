// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package ui

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"strings"
	"unicode/utf8"

	"github.com/pipozzz/goliash/internal/store"
)

// Badges show a service's version and state as a small SVG for a README or a wiki,
// where images load without signing in. A badge URL is signed, so it shows only the
// service and environment it was made for; nobody can make one up for another.

// badgeKey is the server's key for signing badge URLs, made on first use.
func (s *Server) badgeKey(ctx context.Context) ([]byte, error) {
	v, err := s.store.ServerSecret(ctx, "badges", func() (string, error) {
		b := make([]byte, 32)
		_, _ = rand.Read(b)
		return base64.StdEncoding.EncodeToString(b), nil
	})
	if err != nil {
		return nil, err
	}
	return base64.StdEncoding.DecodeString(v)
}

func badgeSig(key []byte, workspaceID, service, env string) string {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(workspaceID + "\n" + service + "\n" + env))
	return hex.EncodeToString(mac.Sum(nil))[:24]
}

// badgeURL is the path of a service's badge, for one environment or all ("").
func (s *Server) badgeURL(ctx context.Context, sc store.Scope, service, env string) (string, error) {
	key, err := s.badgeKey(ctx)
	if err != nil {
		return "", err
	}
	q := url.Values{"sig": {badgeSig(key, sc.WorkspaceID, service, env)}}
	if env != "" {
		q.Set("env", env)
	}
	return "/badge/" + sc.WorkspaceID + "/" + url.PathEscape(service) + ".svg?" + q.Encode(), nil
}

// badge serves a badge. It needs no sign-in; the signature is the permission.
func (s *Server) badge(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	wsID, svc := r.PathValue("ws"), strings.TrimSuffix(r.PathValue("file"), ".svg")
	env := r.URL.Query().Get("env")
	key, err := s.badgeKey(ctx)
	if err != nil || !hmac.Equal([]byte(badgeSig(key, wsID, svc, env)), []byte(r.URL.Query().Get("sig"))) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	label, value, state := svc, "unknown", stUnknown
	if env != "" {
		label += " · " + env
	}
	if sc, ok := s.workspaceScope(ctx, wsID); ok {
		if g, err := s.overview(ctx, sc, ""); err == nil {
			value, state = "not running", stNone
			for _, row := range g.Rows {
				if row.Service != svc {
					continue
				}
				c, running, _ := service(row, g.Envs, env)
				if running != "" {
					value, state = running+" · "+tileLabel(c.State), c.State
				}
				if env == "" && running != "" {
					// Every environment: each one's version, the colour of the worst.
					var parts []string
					for i, cell := range row.Cells {
						if envState(cell, row.Latest) != stNone {
							parts = append(parts, g.Envs[i].Name+" "+versionOf(cell))
						}
					}
					value = strings.Join(parts, " · ")
				}
				break
			}
		}
	}
	w.Header().Set("Content-Type", "image/svg+xml")
	// Short, so a README follows a deploy within minutes; GitHub's image proxy honours it.
	w.Header().Set("Cache-Control", "public, max-age=300")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'")
	_, _ = w.Write([]byte(badgeSVG(label, value, state)))
}

// workspaceScope finds a workspace by ID, across organizations.
func (s *Server) workspaceScope(ctx context.Context, id string) (store.Scope, bool) {
	wss, err := s.store.ListWorkspaces(ctx)
	if err != nil {
		return store.Scope{}, false
	}
	for _, ws := range wss {
		if ws.ID == id {
			return ws.Scope(), true
		}
	}
	return store.Scope{}, false
}

var badgeColours = map[string]string{
	stCurrent: "#2f9e5b", stBehind: "#d98a1c", stAttention: "#d93f44", stUnknown: "#7b8396", stNone: "#9aa1b0",
}

// badgeSVG draws a flat badge: the label on the logo's navy, the value in the state's colour.
func badgeSVG(label, value, state string) string {
	width := func(s string) int { return utf8.RuneCountInString(s)*7 + 12 } // Verdana 11px, roughly
	lw, vw := width(label)+18, width(value)
	colour := badgeColours[state]
	if colour == "" {
		colour = badgeColours[stUnknown]
	}
	l, v := html.EscapeString(label), html.EscapeString(value)
	return fmt.Sprintf(`<svg xmlns="http://www.w3.org/2000/svg" width="%[1]d" height="20" role="img" aria-label="%[4]s: %[5]s">`+
		`<title>%[4]s: %[5]s</title>`+
		`<linearGradient id="g" x2="0" y2="100%%"><stop offset="0" stop-color="#fff" stop-opacity=".12"/><stop offset="1" stop-opacity=".1"/></linearGradient>`+
		`<clipPath id="r"><rect width="%[1]d" height="20" rx="4"/></clipPath>`+
		`<g clip-path="url(#r)"><rect width="%[2]d" height="20" fill="#1b2a6b"/><rect x="%[2]d" width="%[3]d" height="20" fill="%[6]s"/><rect width="%[1]d" height="20" fill="url(#g)"/></g>`+
		`<g transform="translate(5 4)"><rect width="5" height="5" rx="1" fill="#8aa4ff"/><rect x="6" width="5" height="5" rx="1" fill="#8aa4ff" opacity=".55"/><rect y="6" width="5" height="5" rx="1" fill="#8aa4ff" opacity=".55"/><rect x="6" y="6" width="5" height="5" rx="1" fill="#ffb347"/></g>`+
		`<g fill="#fff" text-anchor="middle" font-family="Verdana,DejaVu Sans,Geneva,sans-serif" font-size="11">`+
		`<text x="%[7]d" y="15" fill="#010101" fill-opacity=".3">%[4]s</text><text x="%[7]d" y="14">%[4]s</text>`+
		`<text x="%[8]d" y="15" fill="#010101" fill-opacity=".3">%[5]s</text><text x="%[8]d" y="14">%[5]s</text></g></svg>`,
		lw+vw, lw, vw, l, v, colour, 18+(lw-18)/2, lw+vw/2)
}

// serviceBadges are a service's badges: one for every environment it runs in, and one
// for them all.
func (s *Server) serviceBadges(ctx context.Context, sc store.Scope, service string, envs []ServiceEnv) []BadgeView {
	names := []string{""}
	for _, e := range envs {
		names = append(names, e.Name)
	}
	var out []BadgeView
	for _, env := range names {
		u, err := s.badgeURL(ctx, sc, service, env)
		if err != nil {
			return nil
		}
		abs := s.publicURL + u
		alt := service
		if env != "" {
			alt += " in " + env
		}
		out = append(out, BadgeView{Env: env, URL: u, Markdown: "[![" + alt + "](" + abs + ")](" + s.publicURL + "/services/" + url.PathEscape(service) + ")"})
	}
	return out
}
