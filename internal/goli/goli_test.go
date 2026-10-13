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
	"time"
)

// fakeClaude answers the Messages API with the streams in order and keeps what it was sent.
type fakeClaude struct {
	mu       sync.Mutex
	streams  []string
	requests []map[string]any
	headers  []http.Header
}

func (f *fakeClaude) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	var body map[string]any
	_ = json.Unmarshal(b, &body)
	f.mu.Lock()
	f.requests = append(f.requests, body)
	f.headers = append(f.headers, r.Header.Clone())
	if len(f.streams) == 0 {
		f.mu.Unlock()
		http.Error(w, `{"type":"error","error":{"type":"overloaded_error","message":"x"}}`, 529)
		return
	}
	s := f.streams[0]
	f.streams = f.streams[1:]
	f.mu.Unlock()
	w.Header().Set("Content-Type", "text/event-stream")
	_, _ = io.WriteString(w, s)
}

func sse(events ...string) string {
	var b strings.Builder
	for _, e := range events {
		var t struct{ Type string }
		_ = json.Unmarshal([]byte(e), &t)
		fmt.Fprintf(&b, "event: %s\ndata: %s\n\n", t.Type, e)
	}
	return b.String()
}

const start = `{"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"claude-opus-5-5","content":[],"stop_reason":null,"usage":{"input_tokens":10,"output_tokens":0}}}`

func toolTurn(name, input string) string {
	in, _ := json.Marshal(input)
	return sse(start,
		`{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_1","name":"`+name+`","input":{}}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":`+string(in)+`}}`,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":5}}`,
		`{"type":"message_stop"}`)
}

func textTurn(parts ...string) string {
	evs := []string{start, `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`}
	for _, p := range parts {
		t, _ := json.Marshal(p)
		evs = append(evs, `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":`+string(t)+`}}`)
	}
	evs = append(evs, `{"type":"content_block_stop","index":0}`,
		`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":5}}`, `{"type":"message_stop"}`)
	return sse(evs...)
}

type fakeTools struct {
	calls []string
	input []string
}

func (f *fakeTools) List(context.Context) ([]Tool, error) {
	return []Tool{
		{Name: "status", Description: "data freshness", Schema: map[string]any{"type": "object", "properties": map[string]any{}}},
		{Name: "service", Description: "one service", Schema: map[string]any{
			"type": "object", "properties": map[string]any{"name": map[string]any{"type": "string"}}, "required": []any{"name"},
		}},
	}, nil
}

func (f *fakeTools) Call(_ context.Context, name string, input json.RawMessage) (string, error) {
	f.calls = append(f.calls, name)
	f.input = append(f.input, string(input))
	return `{"data":{"agents":"all fresh"}}`, nil
}

func newTest(t *testing.T, streams ...string) (*Assistant, *fakeClaude) {
	t.Helper()
	fc := &fakeClaude{streams: streams}
	srv := httptest.NewServer(fc)
	t.Cleanup(srv.Close)
	a := New(Config{APIKey: "sk-test", Model: DefaultModel, BaseURL: srv.URL, Effort: "medium", PerHour: 3}, slog.New(slog.DiscardHandler))
	return a, fc
}

func TestAskCallsToolsThenAnswers(t *testing.T) {
	a, fc := newTest(t, toolTurn("service", `{"name":"api"}`), textTurn("All agents ", "report on time. See [status](/status)."))
	tools := &fakeTools{}
	var events []Event
	used, err := a.Ask(context.Background(), tools, []Turn{{Role: "user", Text: "Is the data fresh?"}},
		Context{Workspace: "Prod", Role: "viewer", Now: "2026-10-13T10:00:00Z", Page: "/"}, func(e Event) { events = append(events, e) })
	if err != nil {
		t.Fatal(err)
	}
	if len(used) != 1 || used[0] != "service" || tools.input[0] != `{"name":"api"}` {
		t.Fatalf("tools used %v with %v", used, tools.input)
	}
	var text strings.Builder
	for _, e := range events {
		if e.Type == "text" {
			text.WriteString(e.Text)
		}
	}
	if text.String() != "All agents report on time. See [status](/status)." {
		t.Errorf("answer %q", text.String())
	}
	if events[0].Type != "tool" || events[0].Tool != "service" || events[len(events)-1].Type != "done" {
		t.Errorf("events %+v", events)
	}
	if len(fc.requests) != 2 {
		t.Fatalf("%d requests", len(fc.requests))
	}
	first := fc.requests[0]
	if first["model"] != "claude-opus-5-5" || first["stream"] != true {
		t.Errorf("model %v stream %v", first["model"], first["stream"])
	}
	if _, ok := first["fallbacks"]; !ok {
		t.Error("no refusal fallbacks")
	}
	if !strings.Contains(fc.headers[0].Get("Anthropic-Beta"), "server-side-fallback") {
		t.Errorf("beta header %q", fc.headers[0].Get("Anthropic-Beta"))
	}
	if _, ok := first["thinking"]; ok {
		t.Error("thinking is always on for Opus 5.5 and must not be sent")
	}
	sys, _ := json.Marshal(first["system"])
	if !strings.Contains(string(sys), "cache_control") || !strings.Contains(string(sys), "looking at /") {
		t.Errorf("system %s", sys)
	}
	tl, _ := json.Marshal(first["tools"])
	if !strings.Contains(string(tl), `"required":["name"]`) {
		t.Errorf("tools %s", tl)
	}
	// The second request carries the tool result after the tool use.
	msgs, _ := json.Marshal(fc.requests[1]["messages"])
	if !strings.Contains(string(msgs), `"tool_result"`) || !strings.Contains(string(msgs), "toolu_1") {
		t.Errorf("messages %s", msgs)
	}
}

func TestAskRefusesBadHistory(t *testing.T) {
	a, _ := newTest(t)
	if _, err := a.Ask(context.Background(), &fakeTools{}, []Turn{{Role: "assistant", Text: "hi"}}, Context{}, func(Event) {}); err == nil {
		t.Error("answered an assistant turn")
	}
}

func TestAskStopsAfterTooManyRounds(t *testing.T) {
	var streams []string
	for range maxRounds {
		streams = append(streams, toolTurn("status", `{}`))
	}
	a, _ := newTest(t, streams...)
	var last Event
	if _, err := a.Ask(context.Background(), &fakeTools{}, []Turn{{Role: "user", Text: "loop"}}, Context{}, func(e Event) { last = e }); err != nil {
		t.Fatal(err)
	}
	if last.Type != "error" {
		t.Errorf("last event %+v", last)
	}
}

func TestAskReportsAPIErrors(t *testing.T) {
	a, _ := newTest(t)
	_, err := a.Ask(context.Background(), &fakeTools{}, []Turn{{Role: "user", Text: "hi"}}, Context{}, func(Event) {})
	if err == nil || !strings.Contains(Describe(err), "overloaded") {
		t.Errorf("err %v: %q", err, Describe(err))
	}
}

func TestAllowLimitsPerHour(t *testing.T) {
	a, _ := newTest(t)
	now := time.Now()
	for i := range 3 {
		if !a.Allow("u1", now.Add(time.Duration(i)*time.Minute)) {
			t.Fatalf("question %d refused", i+1)
		}
	}
	if a.Allow("u1", now.Add(5*time.Minute)) {
		t.Error("fourth question allowed")
	}
	if !a.Allow("u2", now) {
		t.Error("someone else refused")
	}
	if !a.Allow("u1", now.Add(61*time.Minute)) {
		t.Error("refused an hour later")
	}
}

func TestConfigFromEnv(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-ignored")
	t.Setenv("GOLIASH_AI_API_KEY", "")
	if _, on, err := ConfigFromEnv(); on || err != nil {
		t.Errorf("on %v err %v without GOLIASH_AI_API_KEY", on, err)
	}
	t.Setenv("GOLIASH_AI_API_KEY", " sk-1 ")
	c, on, err := ConfigFromEnv()
	if !on || err != nil || c.APIKey != "sk-1" || c.Model != DefaultModel || c.Effort != "medium" || c.PerHour != 60 {
		t.Errorf("%+v %v %v", c, on, err)
	}
	t.Setenv("GOLIASH_AI_EFFORT", "max")
	if _, _, err := ConfigFromEnv(); err == nil {
		t.Error("accepted effort max")
	}
}

func TestAllowTodayCeiling(t *testing.T) {
	a, _ := newTest(t)
	now := time.Date(2026, 10, 13, 23, 0, 0, 0, time.UTC)
	if !a.AllowToday(now) {
		t.Fatal("no ceiling refused")
	}
	a.cfg.PerDay = 2
	a.today, a.day = 0, ""
	got := []bool{a.AllowToday(now), a.AllowToday(now), a.AllowToday(now)}
	if !got[0] || !got[1] || got[2] {
		t.Errorf("ceiling of 2: %v", got)
	}
	if !a.AllowToday(now.Add(2 * time.Hour)) {
		t.Error("refused the next day")
	}
}
