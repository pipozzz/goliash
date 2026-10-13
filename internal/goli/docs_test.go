// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package goli

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestDocsAnswersFromTheDocumentation(t *testing.T) {
	var reads atomic.Int32
	docsSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reads.Add(1)
		_, _ = io.WriteString(w, "# Install\nSource: /install/\nRun docker run ghcr.io/pipozzz/goliash try.")
	}))
	defer docsSrv.Close()
	a, fc := newTest(t, textTurn("Run `goliash try`. See [Install](/install/)."), textTurn("Again."))
	d := NewDocs(a, DocsConfig{URL: docsSrv.URL, Origins: []string{"https://goliash.dev"}, PerHour: 1, PerDay: 2},
		func(r *http.Request) string { return r.Header.Get("X-Test-IP") }, slog.New(slog.DiscardHandler))
	srv := httptest.NewServer(d)
	defer srv.Close()

	type answer struct {
		StatusCode int
		Header     http.Header
	}
	ask := func(origin, ip, body string) (answer, string) {
		req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		req.Header.Set("X-Test-IP", ip)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		return answer{resp.StatusCode, resp.Header}, string(b)
	}
	const q = `{"history":[{"role":"user","text":"How do I try it?"}]}`

	resp, body := ask("https://goliash.dev", "1.1.1.1", q)
	if resp.StatusCode != 200 || !strings.Contains(body, "goliash try") || !strings.Contains(body, `"type":"done"`) {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
	if resp.Header.Get("Access-Control-Allow-Origin") != "https://goliash.dev" {
		t.Errorf("CORS %q", resp.Header.Get("Access-Control-Allow-Origin"))
	}
	sent := fc.requests[0]
	sys, _ := jsonString(sent["system"])
	if !strings.Contains(sys, "Source: /install/") || !strings.Contains(sys, "cache_control") {
		t.Errorf("system %s", sys)
	}
	if _, ok := sent["tools"]; ok {
		t.Error("the docs assistant has tools")
	}

	if resp, _ := ask("https://goliash.dev", "1.1.1.1", q); resp.StatusCode != http.StatusTooManyRequests {
		t.Errorf("second question from one address: %d", resp.StatusCode)
	}
	if resp, _ := ask("https://evil.example", "2.2.2.2", q); resp.StatusCode != http.StatusForbidden {
		t.Errorf("other site: %d", resp.StatusCode)
	}
	if resp, _ := ask("https://goliash.dev", "3.3.3.3", `{"history":[{"role":"user","text":"`+strings.Repeat("a", 1001)+`"}]}`); resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Errorf("long question: %d", resp.StatusCode)
	}
	if resp, _ := ask("https://goliash.dev", "4.4.4.4", q); resp.StatusCode != 200 {
		t.Errorf("second address: %d", resp.StatusCode)
	}
	if resp, _ := ask("https://goliash.dev", "5.5.5.5", q); resp.StatusCode != http.StatusTooManyRequests {
		t.Errorf("over the daily ceiling: %d", resp.StatusCode)
	}
	if reads.Load() != 1 {
		t.Errorf("documentation read %d times", reads.Load())
	}

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodOptions, srv.URL, nil)
	req.Header.Set("Origin", "https://goliash.dev")
	pre, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = pre.Body.Close()
	if pre.StatusCode != http.StatusNoContent || pre.Header.Get("Access-Control-Allow-Methods") == "" {
		t.Errorf("preflight %d %v", pre.StatusCode, pre.Header)
	}
}

func TestDocsConfigFromEnv(t *testing.T) {
	t.Setenv("GOLIASH_DOCS_ASSISTANT", "")
	if _, on, err := DocsConfigFromEnv(); on || err != nil {
		t.Errorf("on %v err %v", on, err)
	}
	t.Setenv("GOLIASH_DOCS_ASSISTANT", "https://goliash.dev/llms-full.txt")
	t.Setenv("GOLIASH_DOCS_ASSISTANT_ORIGINS", "https://goliash.dev/, http://localhost:4321")
	c, on, err := DocsConfigFromEnv()
	if !on || err != nil || len(c.Origins) != 2 || c.Origins[0] != "https://goliash.dev" || c.PerHour != 20 || c.PerDay != 500 {
		t.Errorf("%+v %v %v", c, on, err)
	}
	t.Setenv("GOLIASH_DOCS_ASSISTANT_PER_DAY", "0")
	if _, _, err := DocsConfigFromEnv(); err == nil {
		t.Error("accepted a zero ceiling")
	}
}

func jsonString(v any) (string, error) {
	b, err := json.Marshal(v)
	return string(b), err
}
