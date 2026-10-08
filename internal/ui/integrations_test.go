// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package ui

import (
	"encoding/json"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
)

func TestIntegrations(t *testing.T) {
	e := newUIEnv(t)
	host := strings.TrimPrefix(e.srv.URL, "http://")

	viewer := e.as("viewer")
	code, page := get(t, viewer, e.srv.URL+"/integrations", nil)
	if code != 200 {
		t.Fatalf("integrations %d", code)
	}
	for _, want := range []string{"Prometheus and Grafana", "SigNoz", "AI assistants", "targets: [" + host + "]", "scheme: http", "prometheus/goliash", e.srv.URL + "/mcp"} {
		if !strings.Contains(page, want) {
			t.Errorf("page misses %q", want)
		}
	}
	if strings.Contains(page, "Create a viewer token") {
		t.Error("a viewer is offered tokens")
	}
	if code, _, _ := post(t, viewer, e.srv.URL+"/integrations/token", url.Values{"tool": {"signoz"}}); code != http.StatusForbidden {
		t.Errorf("viewer made a token: %d", code)
	}
	for _, f := range []string{"grafana.json", "signoz.json"} {
		code, body := get(t, viewer, e.srv.URL+"/integrations/dashboards/"+f, nil)
		if code != 200 || !json.Valid([]byte(body)) || !strings.Contains(body, "goliash_outdated") {
			t.Errorf("%s: %d", f, code)
		}
	}
	if code, _ := get(t, viewer, e.srv.URL+"/integrations/dashboards/other.json", nil); code != http.StatusNotFound {
		t.Errorf("unknown dashboard %d", code)
	}

	admin := e.as("admin")
	if _, page := get(t, admin, e.srv.URL+"/integrations", nil); strings.Count(page, "Create a viewer token") != 3 {
		t.Error("admin is not offered tokens")
	}
	_, body, _ := post(t, admin, e.srv.URL+"/integrations/token", url.Values{"tool": {"signoz"}})
	token := regexp.MustCompile(`glsh_api_[A-Za-z0-9_-]{20,}`).FindString(body)
	if token == "" || !strings.Contains(body, "/etc/otelcol/goliash-token") || strings.Count(body, token) < 2 {
		t.Fatalf("token not shown: %s", body)
	}
	toks, _ := e.st.ListAPITokens(t.Context(), e.ws.Scope())
	if len(toks) != 1 || toks[0].Name != "signoz" || toks[0].Role != "viewer" {
		t.Fatalf("tokens %+v", toks)
	}
	if _, body, _ := post(t, admin, e.srv.URL+"/integrations/token", url.Values{"tool": {"admin"}}); !strings.Contains(body, "Unknown integration.") {
		t.Error("token for an unknown tool")
	}
}

func TestScrapeTarget(t *testing.T) {
	for in, want := range map[string][3]string{
		"https://goliash.example.com": {"https", "goliash.example.com", "/metrics"},
		"http://10.0.0.5:8080":        {"http", "10.0.0.5:8080", "/metrics"},
		"https://example.com/goliash": {"https", "example.com", "/goliash/metrics"},
		"":                            {"https", "goliash.example.com", "/metrics"},
	} {
		s, h, p := scrapeTarget(in)
		if [3]string{s, h, p} != want {
			t.Errorf("%q: %s %s %s", in, s, h, p)
		}
	}
	if c := collectorConfig("https://example.com/goliash"); !strings.Contains(c, "\n          metrics_path: /goliash/metrics\n") {
		t.Errorf("collector config with a path:\n%s", c)
	}
	if c := prometheusConfig("https://example.com/goliash"); !strings.Contains(c, "\n    metrics_path: /goliash/metrics\n") {
		t.Errorf("prometheus config with a path:\n%s", c)
	}
}
