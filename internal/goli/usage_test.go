// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package goli

import (
	"context"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestUsageCountsQuestionsAndTokens(t *testing.T) {
	a, _ := newTest(t, toolTurn("status", `{}`), textTurn("fine"))
	if !a.AllowToday(time.Now()) {
		t.Fatal("refused")
	}
	if _, err := a.Ask(context.Background(), &fakeTools{}, []Turn{{Role: "user", Text: "ok?"}}, Context{}, func(Event) {}); err != nil {
		t.Fatal(err)
	}
	u := a.Usage()
	// The fake answers each call with 10 input and 5 output tokens.
	if u.Questions != 1 || u.Input != 20 || u.Output != 10 || u.PerDay != 0 {
		t.Errorf("%+v", u)
	}
	if s := u.Summary(); !strings.Contains(s, "1 question,") || !strings.Contains(s, "20 input tokens") {
		t.Errorf("summary %q", s)
	}
	a.cfg.PerDay = 30
	a.record(1_500_000, 1_200_000, 12_345)
	if s := a.Usage().Summary(); !strings.Contains(s, "of 30 allowed") || !strings.Contains(s, "1.5M input tokens (1.2M from cache), 12k output") {
		t.Errorf("summary %q", s)
	}
}

func TestUsageFromOpenAIServers(t *testing.T) {
	chat, _ := newOpenAITest(t, "sk", oaStream(
		`{"choices":[{"delta":{"content":"hi"},"finish_reason":"stop"}]}`,
		`{"choices":[],"usage":{"prompt_tokens":100,"completion_tokens":7,"prompt_tokens_details":{"cached_tokens":80}}}`))
	if _, err := chat.Ask(context.Background(), &fakeTools{}, []Turn{{Role: "user", Text: "hi"}}, Context{}, func(Event) {}); err != nil {
		t.Fatal(err)
	}
	if u := chat.Usage(); u.Input != 100 || u.Cached != 80 || u.Output != 7 {
		t.Errorf("chat %+v", u)
	}

	f := &fakeResponses{streams: []string{rStream(
		`{"type":"response.output_text.delta","delta":"hi"}`,
		`{"type":"response.completed","response":{"status":"completed","usage":{"input_tokens":200,"output_tokens":9,"input_tokens_details":{"cached_tokens":150}}}}`)}}
	srv := httptest.NewServer(f)
	defer srv.Close()
	resp := New(Config{Provider: OpenAI, API: "responses", Model: "m", BaseURL: srv.URL + "/v1", PerHour: 5}, slog.New(slog.DiscardHandler))
	if _, err := resp.Ask(context.Background(), &fakeTools{}, []Turn{{Role: "user", Text: "hi"}}, Context{}, func(Event) {}); err != nil {
		t.Fatal(err)
	}
	if u := resp.Usage(); u.Input != 200 || u.Cached != 150 || u.Output != 9 {
		t.Errorf("responses %+v", u)
	}
}
