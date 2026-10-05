// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package ui

import "net/http"

// contentSecurityPolicy allows only this server's own scripts (no inline ones, so an
// injected <script> or onclick does nothing), its own connections, and no framing.
// Inline styles stay allowed: htmx adds one, and they cannot run code.
const contentSecurityPolicy = "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; " +
	"img-src 'self' data:; connect-src 'self'; font-src 'self'; object-src 'none'; base-uri 'self'; " +
	"form-action 'self'; frame-ancestors 'none'"

// SecurityHeaders sets the headers every response gets. hsts adds
// Strict-Transport-Security, for servers whose public URL is https.
func SecurityHeaders(next http.Handler, hsts bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", contentSecurityPolicy)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "same-origin")
		h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), payment=()")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		if hsts {
			h.Set("Strict-Transport-Security", "max-age=31536000")
		}
		next.ServeHTTP(w, r)
	})
}
