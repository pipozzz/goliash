// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package goli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// fakeOpenAI answers /chat/completions with the streams in order and keeps what it was sent.
type fakeOpenAI struct {
	mu       sync.Mutex
	streams  []string
	requests []map[string]any
	auth     []string
}

func (f *fakeOpenAI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	var body map[string]any
	_ = json.Unmarshal(b, &body)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, body)
	f.auth = append(f.auth, r.Header.Get("Authorization"))
	if r.URL.Path != "/v1/chat/completions" || len(f.streams) == 0 {
		http.Error(w, `{"error":{"message":"no"}}`, http.StatusNotFound)
		return
	}
	s := f.streams[0]
	f.streams = f.streams[1:]
	w.Header().Set("Content-Type", "text/event-stream")
	_, _ = io.WriteString(w, s)
}

func oaStream(chunks ...string) string {
	var b strings.Builder
	for _, c := range chunks {
		fmt.Fprintf(&b, "data: %s\n\n", c)
	}
	b.WriteString("data: [DONE]\n\n")
	return b.String()
}

func newOpenAITest(t *testing.T, key string, streams ...string) (*Assistant, *fakeOpenAI) {
	t.Helper()
	f := &fakeOpenAI{streams: streams}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	a := New(Config{Provider: OpenAI, APIKey: key, Model: "some-model", BaseURL: srv.URL + "/v1", Effort: "medium", PerHour: 5}, slog.New(slog.DiscardHandler))
	return a, f
}

func TestOpenAICallsToolsThenAnswers(t *testing.T) {
	a, f := newOpenAITest(t, "sk-oa",
		oaStream(
			`{"choices":[{"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_a","type":"function","function":{"name":"service","arguments":""}}]}}]}`,
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"name\":"}}]}}]}`,
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"api\"}"}}]}}]}`,
			`{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`),
		oaStream(
			`{"choices":[{"delta":{"content":"api runs "}}]}`,
			`{"choices":[{"delta":{"content":"1.2.3 in prod."}}]}`,
			`{"choices":[{"delta":{},"finish_reason":"stop"}]}`))
	tools := &fakeTools{}
	var text strings.Builder
	var events []Event
	used, err := a.Ask(context.Background(), tools, []Turn{{Role: "user", Text: "What runs?"}},
		Context{Workspace: "Prod", Role: "viewer", Now: "2026-10-13T10:00:00Z", Page: "/"}, func(e Event) {
			events = append(events, e)
			if e.Type == "text" {
				text.WriteString(e.Text)
			}
		})
	if err != nil {
		t.Fatal(err)
	}
	if len(used) != 1 || used[0] != "service" || tools.input[0] != `{"name":"api"}` {
		t.Fatalf("used %v with %v", used, tools.input)
	}
	if text.String() != "api runs 1.2.3 in prod." || events[len(events)-1].Type != "done" {
		t.Errorf("answer %q, events %+v", text.String(), events)
	}
	if len(f.requests) != 2 || f.auth[0] != "Bearer sk-oa" {
		t.Fatalf("%d requests, auth %v", len(f.requests), f.auth)
	}
	first := f.requests[0]
	if first["model"] != "some-model" || first["stream"] != true || first["reasoning_effort"] != nil {
		t.Errorf("request %v", first)
	}
	tl, _ := json.Marshal(first["tools"])
	if !strings.Contains(string(tl), `"type":"function"`) || !strings.Contains(string(tl), `"required":["name"]`) {
		t.Errorf("tools %s", tl)
	}
	msgs, _ := json.Marshal(f.requests[1]["messages"])
	for _, want := range []string{`"role":"tool"`, `"tool_call_id":"call_a"`, `"tool_calls":[{"function":{"arguments":"{\"name\":\"api\"}","name":"service"},"id":"call_a"`, "all fresh", "looking at /"} {
		if !strings.Contains(string(msgs), want) {
			t.Errorf("second request lacks %s: %s", want, msgs)
		}
	}
}

func TestOpenAIErrorsAndLocalServers(t *testing.T) {
	a, f := newOpenAITest(t, "") // a local server without a key
	_, err := a.Ask(context.Background(), &fakeTools{}, []Turn{{Role: "user", Text: "hi"}}, Context{}, func(Event) {})
	if err == nil || !strings.Contains(Describe(err), "not found") {
		t.Errorf("err %v: %q", err, Describe(err))
	}
	if f.auth[0] != "" {
		t.Errorf("sent a key: %q", f.auth[0])
	}
	if a.Via() == "OpenAI" || !strings.HasPrefix(a.Via(), "127.0.0.1") {
		t.Errorf("via %q", a.Via())
	}
}

func TestDocsThroughOpenAI(t *testing.T) {
	docsSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "# Install\nSource: /install/\n")
	}))
	defer docsSrv.Close()
	a, f := newOpenAITest(t, "sk-oa", oaStream(`{"choices":[{"delta":{"content":"See [Install](/install/)."},"finish_reason":"stop"}]}`))
	d := NewDocs(a, DocsConfig{URL: docsSrv.URL, Origins: []string{"https://goliash.dev"}, PerHour: 5, PerDay: 5},
		func(*http.Request) string { return "1.1.1.1" }, slog.New(slog.DiscardHandler))
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/goli/docs", strings.NewReader(`{"history":[{"role":"user","text":"install?"}]}`))
	w := httptest.NewRecorder()
	d.ServeHTTP(w, req)
	if !strings.Contains(w.Body.String(), "See [Install](/install/).") || !strings.Contains(w.Body.String(), `"type":"done"`) {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	sys, _ := json.Marshal(f.requests[0]["messages"])
	if !strings.Contains(string(sys), "Source: /install/") || f.requests[0]["tools"] != nil {
		t.Errorf("messages %s, tools %v", sys, f.requests[0]["tools"])
	}
}

func TestOpenAIConfig(t *testing.T) {
	t.Setenv("GOLIASH_AI_API_KEY", "")
	t.Setenv("GOLIASH_AI_PROVIDER", "openai")
	t.Setenv("GOLIASH_AI_BASE_URL", "http://ollama:11434/v1/")
	t.Setenv("GOLIASH_AI_MODEL", "")
	if _, _, err := ConfigFromEnv(); err == nil {
		t.Error("an OpenAI-compatible server without a model")
	}
	t.Setenv("GOLIASH_AI_MODEL", "llama")
	t.Setenv("GOLIASH_AI_PER_DAY", "300")
	c, on, err := ConfigFromEnv()
	if c.PerDay != 300 {
		t.Errorf("per day %d", c.PerDay)
	}
	if err != nil || !on || c.BaseURL != "http://ollama:11434/v1" || c.EffortSet {
		t.Errorf("%+v %v %v", c, on, err)
	}
	t.Setenv("GOLIASH_AI_PROVIDER", "gemini")
	if _, _, err := ConfigFromEnv(); err == nil {
		t.Error("accepted an unknown provider")
	}
	t.Setenv("GOLIASH_AI_PROVIDER", "")
	t.Setenv("GOLIASH_AI_BASE_URL", "")
	if _, on, _ := ConfigFromEnv(); on {
		t.Error("Anthropic without a key is on")
	}
}
