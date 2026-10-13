// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package ui

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pipozzz/goliash/internal/api"
	"github.com/pipozzz/goliash/internal/goli"
	"github.com/pipozzz/goliash/internal/store"
)

const goliToolTurn = "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"m1\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"claude-opus-5-5\",\"content\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\n" +
	"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":\"toolu_1\",\"name\":\"matrix\",\"input\":{}}}\n\n" +
	"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{}\"}}\n\n" +
	"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n" +
	"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"tool_use\"},\"usage\":{\"output_tokens\":1}}\n\n" +
	"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"

const goliTextTurn = "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"m2\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"claude-opus-5-5\",\"content\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\n" +
	"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n" +
	"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"nginx runs 1.27.2 in prod.\"}}\n\n" +
	"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n" +
	"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":1}}\n\n" +
	"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"

func TestGoliAnswersFromTheAPIAsTheViewer(t *testing.T) {
	e := newUIEnv(t)
	viewer := e.as(store.RoleViewer)
	ask := func() (int, string) {
		req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, e.srv.URL+"/ui/goli",
			strings.NewReader(`{"history":[{"role":"user","text":"Which nginx runs in prod?"}],"page":"/tiles"}`))
		req.Header.Set("Content-Type", "application/json")
		resp, err := viewer.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	if code, _ := ask(); code != http.StatusNotFound {
		t.Fatalf("without Goli: %d", code)
	}
	if _, body := get(t, viewer, e.srv.URL+"/", nil); strings.Contains(body, "data-goli") || strings.Contains(body, "goli-fab") {
		t.Error("the page offers Goli while it is off")
	}
	if _, body := get(t, viewer, e.srv.URL+"/status", nil); !strings.Contains(body, "GOLIASH_AI_API_KEY") {
		t.Error("the status page does not say how to turn Goli on")
	}

	var mu sync.Mutex
	var sent []string
	turns := []string{goliToolTurn, goliTextTurn}
	claude := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		sent = append(sent, string(b))
		turn := turns[0]
		turns = turns[1:]
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, turn)
	}))
	defer claude.Close()
	log := slog.New(slog.DiscardHandler)
	apiMux := http.NewServeMux()
	api.NewPublicHandler(e.st, e.auth, log).Register(apiMux)
	e.ui.goli = goli.New(goli.Config{APIKey: "sk-test", Model: goli.DefaultModel, BaseURL: claude.URL, Effort: "medium", PerHour: 1}, log)
	e.ui.api = apiMux

	if _, body := get(t, viewer, e.srv.URL+"/", nil); !strings.Contains(body, "data-goli") || !strings.Contains(body, "goli-fab") {
		t.Error("the page does not offer Goli")
	}
	if _, body := get(t, viewer, e.srv.URL+"/status", nil); !strings.Contains(body, "answering with claude-opus-5-5") || !strings.Contains(body, "Today: 0 questions") {
		t.Error("the status page does not say Goli is on and what it used")
	}
	code, body := ask()
	if code != http.StatusOK {
		t.Fatalf("%d %s", code, body)
	}
	if !strings.Contains(body, `"type":"tool","tool":"matrix"`) || !strings.Contains(body, "nginx runs 1.27.2 in prod.") || !strings.Contains(body, `"type":"done"`) {
		t.Errorf("stream:\n%s", body)
	}
	if len(sent) != 2 {
		t.Fatalf("%d requests to Claude", len(sent))
	}
	// The matrix tool read the real API with the viewer's session: the tool result carries the workspace's data.
	if !strings.Contains(sent[1], "tool_result") || !strings.Contains(sent[1], "1.27.2") {
		t.Errorf("second request:\n%s", sent[1])
	}
	if strings.Contains(sent[0], "acknowledge") {
		t.Error("Goli was offered the acknowledge tool")
	}
	if !strings.Contains(sent[0], "looking at /tiles") {
		t.Error("Goli was not told the page")
	}
	if code, _ := ask(); code != http.StatusTooManyRequests {
		t.Errorf("over the hourly limit: %d", code)
	}
}

func TestGoliRefusesBadQuestions(t *testing.T) {
	e := newUIEnv(t)
	e.ui.goli = goli.New(goli.Config{APIKey: "sk-test", Model: goli.DefaultModel, BaseURL: "http://127.0.0.1:1", Effort: "medium", PerHour: 10}, slog.New(slog.DiscardHandler))
	e.ui.api = http.NotFoundHandler()
	viewer := e.as(store.RoleViewer)
	for _, body := range []string{
		`nope`,
		`{"history":[]}`,
		`{"history":[{"role":"assistant","text":"hi"}]}`,
		`{"history":[{"role":"system","text":"x"},{"role":"user","text":"hi"}]}`,
		`{"history":[{"role":"user","text":"` + strings.Repeat("a", 2001) + `"}]}`,
	} {
		req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, e.srv.URL+"/ui/goli", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		resp, err := viewer.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode < 400 || resp.StatusCode >= 500 {
			t.Errorf("%.40s: %d", body, resp.StatusCode)
		}
	}
}

func TestGoliOnThePublicDemo(t *testing.T) {
	e := newUIEnv(t)
	ctx := context.Background()
	viewer, err := e.st.CreateUser(ctx, e.ws.OrgID, "demo@goliash.dev", "", store.RoleViewer)
	if err != nil {
		t.Fatal(err)
	}
	_ = e.st.SetMembership(ctx, viewer.ID, e.ws.ID, store.RoleViewer)
	e.auth.SetDemoUser(viewer)
	guarded := httptest.NewServer(DemoGuard(e.srv.Config.Handler))
	defer guarded.Close()

	var mu sync.Mutex
	var sent []string
	claude := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		sent = append(sent, string(b))
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, goliTextTurn)
	}))
	defer claude.Close()
	log := slog.New(slog.DiscardHandler)
	apiMux := http.NewServeMux()
	api.NewPublicHandler(e.st, e.auth, log).Register(apiMux)
	e.ui.goli = goli.New(goli.Config{APIKey: "sk-test", Model: goli.DefaultModel, BaseURL: claude.URL, Effort: "medium", PerHour: 1}, log)
	e.ui.api = apiMux

	jar, _ := cookiejar.New(nil)
	visitor := &http.Client{Jar: jar}
	question := "What should we upgrade first?"
	ask := func() int {
		req, _ := http.NewRequestWithContext(ctx, http.MethodPost, guarded.URL+"/ui/goli",
			strings.NewReader(`{"history":[{"role":"user","text":"`+question+`"}],"page":"/updates"}`))
		req.Header.Set("Content-Type", "application/json")
		resp, err := visitor.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		return resp.StatusCode
	}

	// Off on the demo unless asked for.
	if _, body := get(t, visitor, guarded.URL+"/updates", nil); strings.Contains(body, "data-goli") {
		t.Error("the demo offers Goli without GOLIASH_DEMO_GOLI")
	}
	if code := ask(); code != http.StatusNotFound {
		t.Errorf("demo without Goli: %d", code)
	}

	e.ui.goliDemo = true
	if _, body := get(t, visitor, guarded.URL+"/updates", nil); !strings.Contains(body, "data-goli") {
		t.Error("the demo does not offer Goli")
	}
	if code := ask(); code != http.StatusOK {
		t.Fatalf("demo with Goli: %d", code)
	}
	if len(sent) != 1 || !strings.Contains(sent[0], "public demo") {
		t.Errorf("Goli was not told it is on the demo: %d requests", len(sent))
	}
	// The same first question again is answered from the cache: no model call, no limit spent.
	if code := ask(); code != http.StatusOK || len(sent) != 1 {
		t.Errorf("asked again: %d, %d model calls", code, len(sent))
	}
	// Every visitor is the same viewer: the hourly limit counts per address.
	question = "Something else?"
	if code := ask(); code != http.StatusTooManyRequests {
		t.Errorf("another question from one address: %d", code)
	}
}

func TestGoliCache(t *testing.T) {
	var c goliCache
	now := time.Now()
	k := goliCacheKey("ws", "  What   runs?  ")
	if k != goliCacheKey("ws", "what runs?") || k == goliCacheKey("other", "what runs?") {
		t.Error("keys")
	}
	c.put(k, []goli.Event{{Type: "text", Text: "x"}, {Type: "done"}}, now)
	if ev, ok := c.get(k, now.Add(time.Hour)); !ok || len(ev) != 2 {
		t.Error("miss within the hour")
	}
	if _, ok := c.get(k, now.Add(goliCacheTTL+time.Minute)); ok {
		t.Error("hit after the TTL")
	}
	for i := range goliCacheSize + 5 {
		c.put(fmt.Sprint(i), nil, now.Add(time.Duration(i)*time.Second))
	}
	if len(c.m) > goliCacheSize {
		t.Errorf("%d entries", len(c.m))
	}
}
