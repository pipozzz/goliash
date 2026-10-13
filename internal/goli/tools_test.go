// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package goli

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMCPToolsReadAsThePersonAsking(t *testing.T) {
	var seen []*http.Request
	api := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"agents":[],"upstream":{"pending":0}}`))
	})
	from := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/ui/goli", nil)
	from.AddCookie(&http.Cookie{Name: "goliash_session", Value: "s3cret"})
	ctx := context.Background()
	tools, err := NewMCPTools(ctx, api, from)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tools.Close() }()

	list, err := tools.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, tl := range list {
		names[tl.Name] = true
		if tl.Schema["type"] != "object" {
			t.Errorf("%s schema %v", tl.Name, tl.Schema)
		}
	}
	if names["acknowledge"] || !names["status"] || !names["matrix"] || len(list) != len(readOnly) {
		t.Errorf("tools %v", names)
	}
	out, err := tools.Call(ctx, "status", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "upstream") {
		t.Errorf("status said %s", out)
	}
	if len(seen) != 1 {
		t.Fatalf("%d API calls", len(seen))
	}
	r := seen[0]
	if r.Header.Get("Authorization") != "" {
		t.Errorf("sent Authorization %q", r.Header.Get("Authorization"))
	}
	if c, err := r.Cookie("goliash_session"); err != nil || c.Value != "s3cret" {
		t.Errorf("session cookie %v %v", c, err)
	}
	if r.Method != http.MethodGet || !strings.HasPrefix(r.URL.Path, "/api/v1/status") {
		t.Errorf("%s %s", r.Method, r.URL)
	}
	if _, err := tools.Call(ctx, "acknowledge", json.RawMessage(`{"service":"api"}`)); err == nil {
		t.Error("acknowledged")
	}
}

func TestInProcessOnlyReads(t *testing.T) {
	p := inProcess{api: http.NotFoundHandler()}
	r := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "http://goli.internal/api/v1/acks", strings.NewReader("{}"))
	resp, err := p.RoundTrip(r)
	if err == nil {
		_ = resp.Body.Close()
		t.Error("posted")
	}
}
