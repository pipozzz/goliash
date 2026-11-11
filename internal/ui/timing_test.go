// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package ui

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestTiming(t *testing.T) {
	var logs bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logs, nil))
	h := Timing(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/slow" {
			time.Sleep(slowRequest + 50*time.Millisecond)
		}
		_, _ = w.Write([]byte("ok"))
	}), log)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/fast", nil))
	if !strings.HasPrefix(rec.Header().Get("Server-Timing"), "app;dur=") || logs.Len() != 0 {
		t.Fatalf("fast: %q %q", rec.Header().Get("Server-Timing"), logs.String())
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/slow", nil))
	if !strings.Contains(logs.String(), "slow request") || !strings.Contains(logs.String(), "path=/slow") {
		t.Fatalf("slow not logged: %q", logs.String())
	}
}
