// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package auth

import (
	"net/http"
	"strings"
	"time"
)

// landingCookie remembers the page someone asked for before signing in, so signing in brings them back to it
// (an app asking for access, a link from an alert) instead of the matrix.
const landingCookie = "goliash_next"

// RememberLanding keeps where to go after signing in: a path on this server, never another site.
func (a *Auth) RememberLanding(w http.ResponseWriter, r *http.Request) {
	path := r.URL.RequestURI()
	if !localPath(path) || len(path) > 4000 {
		return
	}
	http.SetCookie(w, &http.Cookie{ //nolint:gosec // Secure follows the public URL scheme; plain http is for local use
		Name: landingCookie, Value: path, Path: "/", HttpOnly: true, Secure: a.publicURL.Scheme == "https",
		SameSite: http.SameSiteLaxMode, MaxAge: int((15 * time.Minute).Seconds()),
	})
}

// landing is where a sign-in ends: the remembered page, once, or the matrix.
func (a *Auth) landing(w http.ResponseWriter, r *http.Request) string {
	c, err := r.Cookie(landingCookie)
	if err != nil {
		return "/"
	}
	http.SetCookie(w, &http.Cookie{ //nolint:gosec // Secure follows the public URL scheme; plain http is for local use
		Name: landingCookie, Path: "/", MaxAge: -1, HttpOnly: true, Secure: a.publicURL.Scheme == "https",
		SameSite: http.SameSiteLaxMode,
	})
	// Not the UI's own endpoints (the live stream, partials): earlier releases could remember those.
	if !localPath(c.Value) || strings.HasPrefix(c.Value, "/ui/") {
		return "/"
	}
	return c.Value
}

// localPath reports whether p is a path on this server: "/x", not "//host" or "/\host", which browsers read as
// another site.
func localPath(p string) bool {
	return strings.HasPrefix(p, "/") && !strings.HasPrefix(p, "//") && !strings.HasPrefix(p, "/\\") &&
		!strings.ContainsAny(p, "\r\n")
}
