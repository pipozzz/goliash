// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package goli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
)

// DocsConfig is Goli answering questions about Goliash itself, for a website: from its documentation only,
// for anyone, with no access to any data.
type DocsConfig struct {
	URL     string   // the documentation as one text, e.g. https://goliash.dev/llms-full.txt
	Origins []string // the sites that may ask, e.g. https://goliash.dev
	PerHour int      // questions one address may ask per hour
	PerDay  int      // questions everyone together may ask per day: a ceiling on the cost
	Model   string   // a model of its own (often a cheaper one); empty for Goli's
}

// DocsConfigFromEnv reads GOLIASH_DOCS_ASSISTANT (the documentation's URL), GOLIASH_DOCS_ASSISTANT_ORIGINS
// (comma-separated; default https://goliash.dev), GOLIASH_DOCS_ASSISTANT_PER_HOUR (20),
// GOLIASH_DOCS_ASSISTANT_PER_DAY (500) and GOLIASH_DOCS_ASSISTANT_MODEL (Goli's own model when empty). ok is false
// without a URL.
func DocsConfigFromEnv() (DocsConfig, bool, error) {
	c := DocsConfig{
		URL: strings.TrimSpace(os.Getenv("GOLIASH_DOCS_ASSISTANT")), Origins: []string{"https://goliash.dev"}, PerHour: 20, PerDay: 500,
		Model: strings.TrimSpace(os.Getenv("GOLIASH_DOCS_ASSISTANT_MODEL")),
	}
	if c.URL == "" {
		return c, false, nil
	}
	if !strings.HasPrefix(c.URL, "https://") && !strings.HasPrefix(c.URL, "http://") {
		return c, false, fmt.Errorf("GOLIASH_DOCS_ASSISTANT %q: a URL", c.URL)
	}
	if v := os.Getenv("GOLIASH_DOCS_ASSISTANT_ORIGINS"); v != "" {
		c.Origins = nil
		for _, o := range strings.Split(v, ",") {
			if o = strings.TrimRight(strings.TrimSpace(o), "/"); o != "" {
				c.Origins = append(c.Origins, o)
			}
		}
	}
	for name, n := range map[string]*int{"GOLIASH_DOCS_ASSISTANT_PER_HOUR": &c.PerHour, "GOLIASH_DOCS_ASSISTANT_PER_DAY": &c.PerDay} {
		if v := os.Getenv(name); v != "" {
			if _, err := fmt.Sscan(v, n); err != nil || *n < 1 {
				return c, false, fmt.Errorf("%s %q: a positive number", name, v)
			}
		}
	}
	return c, true, nil
}

// Docs answers questions about Goliash from its documentation.
type Docs struct {
	a        *Assistant
	cfg      DocsConfig
	clientIP func(*http.Request) string
	http     *http.Client
	log      *slog.Logger

	mu      sync.Mutex
	text    string
	fetched time.Time
	day     string // the UTC date the count is for
	today   int
	byAddr  *Assistant // per-address hourly limit, reusing Allow
}

// NewDocs returns the documentation assistant. clientIP tells where a request comes from.
func NewDocs(a *Assistant, cfg DocsConfig, clientIP func(*http.Request) string, log *slog.Logger) *Docs {
	// An assistant of its own, so its usage is counted apart from Goli's.
	own := a.cfg
	if cfg.Model != "" {
		own.Model = cfg.Model
	}
	a = New(own, log)
	return &Docs{
		a: a, cfg: cfg, clientIP: clientIP, http: &http.Client{Timeout: 20 * time.Second}, log: log,
		byAddr: &Assistant{cfg: Config{PerHour: cfg.PerHour}, asked: map[string][]time.Time{}},
	}
}

const (
	maxDocsQuestion = 1000
	maxDocsTurns    = 10
	maxDocsText     = 600 << 10
	docsTTL         = time.Hour
)

// docs returns the documentation, read again after an hour; a failed read keeps the last good text.
func (d *Docs) docs(ctx context.Context) (string, error) {
	d.mu.Lock()
	text, fetched := d.text, d.fetched
	d.mu.Unlock()
	if text != "" && time.Since(fetched) < docsTTL {
		return text, nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, d.cfg.URL, nil)
	if err != nil {
		return text, err
	}
	resp, err := d.http.Do(req)
	if err == nil {
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusOK {
			err = fmt.Errorf("%s answered %d", d.cfg.URL, resp.StatusCode)
		}
	}
	if err == nil {
		var b []byte
		if b, err = io.ReadAll(io.LimitReader(resp.Body, maxDocsText)); err == nil && len(b) > 0 {
			text = string(b)
		}
	}
	if err != nil {
		d.log.WarnContext(ctx, "docs assistant: reading the documentation failed", "url", d.cfg.URL, "err", err)
		if text == "" {
			return "", err
		}
	}
	d.mu.Lock()
	d.text, d.fetched = text, time.Now()
	d.mu.Unlock()
	return text, nil
}

// allowDay counts a question against the daily ceiling.
func (d *Docs) allowDay(now time.Time) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if day := now.UTC().Format(time.DateOnly); day != d.day {
		d.day, d.today = day, 0
	}
	if d.today >= d.cfg.PerDay {
		return false
	}
	d.today++
	return true
}

// Usage is what the documentation assistant used today.
func (d *Docs) Usage() Usage {
	u := d.a.Usage()
	d.mu.Lock()
	if d.day == u.Day {
		u.Questions = d.today
	} else {
		u.Questions = 0
	}
	d.mu.Unlock()
	u.PerDay = d.cfg.PerDay
	return u
}

// Model is the model the documentation assistant answers with.
func (d *Docs) Model() string { return d.a.Model() }

func (d *Docs) cors(w http.ResponseWriter, r *http.Request) bool {
	origin := strings.TrimRight(r.Header.Get("Origin"), "/")
	for _, o := range d.cfg.Origins {
		if origin == o {
			h := w.Header()
			h.Set("Access-Control-Allow-Origin", origin)
			h.Set("Access-Control-Allow-Methods", "POST, OPTIONS")
			h.Set("Access-Control-Allow-Headers", "Content-Type")
			h.Set("Access-Control-Max-Age", "86400")
			h.Set("Access-Control-Expose-Headers", "X-Goli-Via")
			h.Add("Vary", "Origin")
			return true
		}
	}
	return false
}

func docsError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// ServeHTTP answers POST {"history": [{"role": "user", "text": …}]} as server-sent events, like /ui/goli.
func (d *Docs) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	allowed := d.cors(w, r)
	// Who answers, for the site to tell its visitors where their questions go.
	w.Header().Set("X-Goli-Via", d.a.Via())
	switch {
	case r.Method == http.MethodOptions:
		w.WriteHeader(http.StatusNoContent)
		return
	case r.Method != http.MethodPost:
		docsError(w, http.StatusMethodNotAllowed, "POST a question")
		return
	case !allowed && r.Header.Get("Origin") != "":
		docsError(w, http.StatusForbidden, "this site may not ask")
		return
	}
	var in struct {
		History []Turn `json:"history"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&in); err != nil {
		docsError(w, http.StatusBadRequest, `send {"history": [{"role": "user", "text": "…"}]}`)
		return
	}
	n := len(in.History)
	if n == 0 || in.History[n-1].Role != "user" || strings.TrimSpace(in.History[n-1].Text) == "" {
		docsError(w, http.StatusBadRequest, "ask something")
		return
	}
	if len([]rune(in.History[n-1].Text)) > maxDocsQuestion {
		docsError(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("ask in at most %d characters", maxDocsQuestion))
		return
	}
	if n > maxDocsTurns {
		in.History = in.History[n-maxDocsTurns:]
	}
	now := time.Now()
	if !d.byAddr.Allow(d.clientIP(r), now) {
		docsError(w, http.StatusTooManyRequests, "Goli needs a break: you asked a lot this hour. The docs are all at goliash.dev.")
		return
	}
	if !d.allowDay(now) {
		docsError(w, http.StatusTooManyRequests, "Goli answered a lot of questions today and rests until tomorrow. The docs are all at goliash.dev.")
		return
	}
	ctx := r.Context()
	docs, err := d.docs(ctx)
	if err != nil {
		docsError(w, http.StatusServiceUnavailable, "Goli cannot read the documentation right now.")
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		docsError(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	emit := func(e Event) {
		b, _ := json.Marshal(e)
		_, _ = fmt.Fprintf(w, "data: %s\n\n", b)
		flusher.Flush()
	}
	start := time.Now()
	err = d.answer(ctx, docs, in.History, emit)
	// How many and how long; never the question.
	d.log.InfoContext(ctx, "goli answered from the docs", "took", time.Since(start).Round(time.Millisecond), "err", err)
	if err != nil {
		emit(Event{Type: "error", Text: Describe(err)})
	}
}

func (d *Docs) answer(ctx context.Context, docs string, history []Turn, emit func(Event)) error {
	var messages []anthropic.BetaMessageParam
	for _, t := range history {
		text := t.Text
		if len(text) > maxTurnText {
			text = text[:maxTurnText]
		}
		if strings.TrimSpace(text) == "" {
			continue
		}
		block := anthropic.NewBetaTextBlock(text)
		switch t.Role {
		case "assistant":
			messages = append(messages, anthropic.BetaMessageParam{Role: anthropic.BetaMessageParamRoleAssistant, Content: []anthropic.BetaContentBlockParamUnion{block}})
		case "user":
			messages = append(messages, anthropic.NewBetaUserMessage(block))
		}
	}
	if len(messages) == 0 {
		return errors.New("nothing to answer")
	}
	if d.a.cfg.Provider == OpenAI && d.a.cfg.API == "responses" {
		_, _, finish, err := d.a.respond(ctx, docsPrompt+"\n\n<documentation>\n"+docs+"\n</documentation>", rHistory(history), nil, emit)
		if err != nil {
			return err
		}
		if finish == "content_filter" {
			emit(Event{Type: "error", Text: "Goli cannot help with that one."})
			return nil
		}
		emit(Event{Type: "done"})
		return nil
	}
	if d.a.cfg.Provider == OpenAI {
		msgs := append([]oaMessage{oaText("system", docsPrompt+"\n\n<documentation>\n"+docs+"\n</documentation>")}, oaHistory(history)...)
		_, finish, err := d.a.chat(ctx, msgs, nil, emit)
		if err != nil {
			return err
		}
		if finish == "content_filter" {
			emit(Event{Type: "error", Text: "Goli cannot help with that one."})
			return nil
		}
		emit(Event{Type: "done"})
		return nil
	}
	stream := d.a.client.Beta.Messages.NewStreaming(ctx, anthropic.BetaMessageNewParams{
		Model: d.a.cfg.Model, MaxTokens: 4000, Messages: messages,
		System: []anthropic.BetaTextBlockParam{
			{Text: docsPrompt},
			{Text: "<documentation>\n" + docs + "\n</documentation>", CacheControl: anthropic.NewBetaCacheControlEphemeralParam()},
		},
		OutputConfig: anthropic.BetaOutputConfigParam{Effort: anthropic.BetaOutputConfigEffortLow},
		Betas:        []anthropic.AnthropicBeta{anthropic.AnthropicBetaServerSideFallback2026_07_01},
		Fallbacks:    anthropic.BetaFallbacksParamOfDefault(),
	})
	msg := anthropic.BetaMessage{}
	for stream.Next() {
		ev := stream.Current()
		if err := msg.Accumulate(ev); err != nil {
			return err
		}
		if dv, ok := ev.AsAny().(anthropic.BetaRawContentBlockDeltaEvent); ok {
			if t, ok := dv.Delta.AsAny().(anthropic.BetaTextDelta); ok && t.Text != "" {
				emit(Event{Type: "text", Text: t.Text})
			}
		}
	}
	if err := stream.Err(); err != nil {
		return err
	}
	d.a.record(msg.Usage.InputTokens+msg.Usage.CacheCreationInputTokens+msg.Usage.CacheReadInputTokens, msg.Usage.CacheReadInputTokens, msg.Usage.OutputTokens)
	if msg.StopReason == anthropic.BetaStopReasonRefusal {
		emit(Event{Type: "error", Text: "Goli cannot help with that one."})
		return nil
	}
	emit(Event{Type: "done"})
	return nil
}

// docsPrompt comes before the documentation; both are the same for every question, so they are cached.
const docsPrompt = `You are Goli, the mascot and assistant of Goliash (goliash.dev): a gentle giant with a release bell, round glasses and one raised eyebrow. Goliash is a self-hosted, open-source tool that shows what runs where, on which version, in which environment, across Kubernetes, Amazon ECS, AWS Lambda, Nomad, Docker Swarm, Docker hosts and Compose, with upstream releases, drift, end of life, vulnerabilities and an SBOM of what runs.

You answer questions from people visiting the website: what Goliash does, whether it fits their setup, how to install, connect, configure and use it, and how it compares in approach. Answer only from the documentation below. When it does not cover something, say so and point to the closest page or to GitHub issues (https://github.com/pipozzz/goliash/issues); never invent options, environment variables, flags, prices or features.

Stay on Goliash and the operations topics around it. For anything unrelated, say kindly in one sentence that you only know about Goliash.

Answer in the language of the question. Keep answers short and concrete: lead with the answer, then the steps or details that matter, with code blocks for commands and configuration. Link to documentation pages as relative paths from the "Source:" lines, e.g. [Install](/install/). Mention the live demo (https://demo.goliash.dev) when seeing it would help. A little warmth is welcome; no jokes at the asker's expense.`
