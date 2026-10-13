// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package goli

import (
	"context"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCompleteThroughEveryProvider(t *testing.T) {
	ctx := context.Background()
	a, fc := newTest(t, textTurn("Upgrade postgres ", "first."))
	got, err := a.Complete(ctx, TakeInstructions, "- postgres in prod: 15 → 17")
	if err != nil || got != "Upgrade postgres first." {
		t.Fatalf("claude %q %v", got, err)
	}
	if _, ok := fc.requests[0]["tools"]; ok || !strings.Contains(asJSON(fc.requests[0]["system"]), "Goli's take") {
		t.Errorf("claude request %v", fc.requests[0])
	}

	chat, _ := newOpenAITest(t, "sk", oaStream(`{"choices":[{"delta":{"content":"Chat take."},"finish_reason":"stop"}]}`))
	if got, err := chat.Complete(ctx, TakeInstructions, "x"); err != nil || got != "Chat take." {
		t.Errorf("chat %q %v", got, err)
	}

	f := &fakeResponses{streams: []string{rStream(`{"type":"response.output_text.delta","delta":"Responses take."}`, `{"type":"response.completed","response":{"status":"completed"}}`)}}
	srv := httptest.NewServer(f)
	defer srv.Close()
	resp := New(Config{Provider: OpenAI, API: "responses", Model: "m", BaseURL: srv.URL + "/v1", PerHour: 1}, slog.New(slog.DiscardHandler))
	if got, err := resp.Complete(ctx, TakeInstructions, "x"); err != nil || got != "Responses take." {
		t.Errorf("responses %q %v", got, err)
	}
}

func asJSON(v any) string { s, _ := jsonString(v); return s }
