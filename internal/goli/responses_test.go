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

type fakeResponses struct {
	mu       sync.Mutex
	streams  []string
	requests []map[string]any
}

func (f *fakeResponses) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	var body map[string]any
	_ = json.Unmarshal(b, &body)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, body)
	if r.URL.Path != "/v1/responses" || len(f.streams) == 0 {
		http.Error(w, `{"error":{"message":"no"}}`, http.StatusNotFound)
		return
	}
	s := f.streams[0]
	f.streams = f.streams[1:]
	w.Header().Set("Content-Type", "text/event-stream")
	_, _ = io.WriteString(w, s)
}

func rStream(events ...string) string {
	var b strings.Builder
	for _, e := range events {
		var t struct{ Type string }
		_ = json.Unmarshal([]byte(e), &t)
		fmt.Fprintf(&b, "event: %s\ndata: %s\n\n", t.Type, e)
	}
	return b.String()
}

func TestResponsesCallsToolsThenAnswers(t *testing.T) {
	f := &fakeResponses{streams: []string{
		rStream(
			`{"type":"response.created","response":{"status":"in_progress"}}`,
			`{"type":"response.output_item.done","item":{"type":"reasoning","id":"rs_1","encrypted_content":"opaque"}}`,
			`{"type":"response.function_call_arguments.delta","delta":"{\"name\":"}`,
			`{"type":"response.output_item.done","item":{"type":"function_call","id":"fc_1","call_id":"call_1","name":"service","arguments":"{\"name\":\"api\"}"}}`,
			`{"type":"response.completed","response":{"status":"completed"}}`),
		rStream(
			`{"type":"response.output_text.delta","delta":"api runs "}`,
			`{"type":"response.output_text.delta","delta":"1.2.3."}`,
			`{"type":"response.output_item.done","item":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"api runs 1.2.3."}]}}`,
			`{"type":"response.completed","response":{"status":"completed"}}`),
	}}
	srv := httptest.NewServer(f)
	defer srv.Close()
	a := New(Config{Provider: OpenAI, API: "responses", APIKey: "sk", Model: "gpt-x", BaseURL: srv.URL + "/v1", Effort: "high", EffortSet: true, PerHour: 5}, slog.New(slog.DiscardHandler))
	tools := &fakeTools{}
	var text strings.Builder
	used, err := a.Ask(context.Background(), tools, []Turn{{Role: "user", Text: "What runs?"}}, Context{Page: "/updates"}, func(e Event) {
		if e.Type == "text" {
			text.WriteString(e.Text)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(used) != 1 || tools.input[0] != `{"name":"api"}` || text.String() != "api runs 1.2.3." {
		t.Fatalf("used %v %v, answer %q", used, tools.input, text.String())
	}
	first := f.requests[0]
	if first["store"] != false || first["stream"] != true || !strings.Contains(fmt.Sprint(first["include"]), "reasoning.encrypted_content") {
		t.Errorf("request %v", first)
	}
	if r, _ := first["reasoning"].(map[string]any); r["effort"] != "high" {
		t.Errorf("reasoning %v", first["reasoning"])
	}
	if !strings.Contains(fmt.Sprint(first["instructions"]), "You are Goli") {
		t.Error("no instructions")
	}
	tl, _ := json.Marshal(first["tools"])
	if !strings.Contains(string(tl), `"name":"service"`) || !strings.Contains(string(tl), `"type":"function"`) {
		t.Errorf("tools %s", tl)
	}
	in, _ := json.Marshal(f.requests[1]["input"])
	for _, want := range []string{`"encrypted_content":"opaque"`, `"call_id":"call_1"`, `"type":"function_call_output"`, "all fresh", "looking at /updates"} {
		if !strings.Contains(string(in), want) {
			t.Errorf("second input lacks %s: %s", want, in)
		}
	}
}

func TestResponsesFailureAndDefaults(t *testing.T) {
	f := &fakeResponses{streams: []string{rStream(`{"type":"response.failed","response":{"status":"failed","error":{"message":"boom"}}}`)}}
	srv := httptest.NewServer(f)
	defer srv.Close()
	a := New(Config{Provider: OpenAI, API: "responses", Model: "gpt-x", BaseURL: srv.URL + "/v1", PerHour: 5}, slog.New(slog.DiscardHandler))
	if _, err := a.Ask(context.Background(), &fakeTools{}, []Turn{{Role: "user", Text: "hi"}}, Context{}, func(Event) {}); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Errorf("err %v", err)
	}
	if f.requests[0]["reasoning"] != nil {
		t.Error("sent an effort nobody chose")
	}
	if New(Config{Provider: OpenAI}, slog.New(slog.DiscardHandler)).cfg.API != "responses" {
		t.Error("OpenAI itself does not default to the Responses API")
	}
	if New(Config{Provider: OpenAI, BaseURL: "http://ollama:11434/v1"}, slog.New(slog.DiscardHandler)).cfg.API != "chat" {
		t.Error("a compatible server does not default to Chat Completions")
	}
}
