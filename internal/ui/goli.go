// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package ui

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/pipozzz/goliash/internal/auth"
	"github.com/pipozzz/goliash/internal/goli"
)

const maxQuestion = 2000 // characters of one question

// goliAsk answers a question with Goli, as a stream of server-sent events: text as it is written, the tools
// Goli looks at, then done or error.
func (s *Server) goliAsk(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	if s.goli == nil || s.auth.Demo() {
		http.NotFound(w, r)
		return nil
	}
	var in struct {
		History []goli.Turn `json:"history"`
		Page    string      `json:"page"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		http.Error(w, "send {\"history\": [{\"role\": \"user\", \"text\": …}]}", http.StatusBadRequest)
		return nil
	}
	if n := len(in.History); n == 0 || in.History[n-1].Role != "user" || strings.TrimSpace(in.History[n-1].Text) == "" {
		http.Error(w, "ask something", http.StatusBadRequest)
		return nil
	}
	for _, t := range in.History {
		if t.Role != "user" && t.Role != "assistant" {
			http.Error(w, "roles are user and assistant", http.StatusBadRequest)
			return nil
		}
	}
	if q := in.History[len(in.History)-1].Text; len([]rune(q)) > maxQuestion {
		http.Error(w, fmt.Sprintf("ask in at most %d characters", maxQuestion), http.StatusRequestEntityTooLarge)
		return nil
	}
	if !s.goli.Allow(p.User.ID, time.Now()) {
		http.Error(w, "You asked Goli a lot this hour; try again later.", http.StatusTooManyRequests)
		return nil
	}
	if !s.goli.AllowToday(time.Now()) {
		http.Error(w, "Goli answered as many questions today as this server allows; ask again tomorrow.", http.StatusTooManyRequests)
		return nil
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		return errors.New("streaming unsupported")
	}
	page := in.Page
	if !strings.HasPrefix(page, "/") || strings.HasPrefix(page, "//") || len(page) > 300 {
		page = "/"
	}

	ctx := r.Context()
	tools, err := goli.NewMCPTools(ctx, s.api, r)
	if err != nil {
		return err
	}
	defer func() { _ = tools.Close() }()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	emit := func(e goli.Event) {
		b, _ := json.Marshal(e)
		_, _ = fmt.Fprintf(w, "data: %s\n\n", b)
		flusher.Flush()
	}
	start := time.Now()
	used, err := s.goli.Ask(ctx, tools, in.History, goli.Context{
		Workspace: p.Workspace.Name, Role: p.Role, Now: start.UTC().Format(time.RFC3339), Page: page,
	}, emit)
	// Who asked and what Goli looked at, never the question: it may hold anything.
	s.log.InfoContext(ctx, "goli answered", "user", p.User.Email, "workspace", p.Workspace.Name,
		"tools", used, "took", time.Since(start).Round(time.Millisecond), "err", err)
	if err != nil {
		emit(goli.Event{Type: "error", Text: goli.Describe(err)})
	}
	return nil
}
