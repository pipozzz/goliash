// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package ui

import "net/http"

// linkConfirm is where a sign-in link leads: a button to sign in, as long as the link is unused and unexpired.
func (s *Server) linkConfirm(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("token")
	u, ok := s.auth.PeekLink(r.Context(), token)
	if !ok {
		http.Redirect(w, r, "/login?error=link", http.StatusSeeOther)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer") // the token is in this page's address
	_ = render(w, r, LinkConfirmPage(LinkConfirmView{Email: u.Email, Token: token}))
}
