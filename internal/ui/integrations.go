// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package ui

import (
	"fmt"
	"net/http"
	"net/url"

	"github.com/pipozzz/goliash/deploy"
	"github.com/pipozzz/goliash/internal/auth"
	"github.com/pipozzz/goliash/internal/store"
	"github.com/pipozzz/goliash/internal/tokens"
)

// IntegrationsView is the Integrations page: Prometheus and Grafana, SigNoz and AI assistants, each with its
// configuration filled in for this server. Right after "Create a token", Tool and Token say for which and what.
type IntegrationsView struct {
	Base
	ServerURL string
	Tool      string
	Token     string
}

// integrationTools are the tools a token can be made for here, with the token's name.
var integrationTools = map[string]string{"prometheus": "prometheus", "signoz": "signoz", "mcp": "assistant"}

func (s *Server) integrations(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	return render(w, r, IntegrationsPage(IntegrationsView{Base: withFlash(s.base(r.Context(), p, "integrations", "Integrations"), r), ServerURL: s.publicURL}))
}

// integrationToken makes a viewer token for one tool and shows the page with it filled in, once.
func (s *Server) integrationToken(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	tool := r.FormValue("tool")
	name, ok := integrationTools[tool]
	if !ok {
		return back(w, r, "/integrations", "error", "Unknown integration.")
	}
	token, hash := tokens.New(tokens.API)
	if _, err := s.store.CreateAPIToken(r.Context(), p.Scope, store.APIToken{Name: name, Role: store.RoleViewer, CreatedBy: p.Name()}, hash); err != nil {
		return err
	}
	s.audit(r.Context(), p, "api_token.create", "token", name, "role", store.RoleViewer, "expires", "never")
	v := IntegrationsView{Base: s.base(r.Context(), p, "integrations", "Integrations"), ServerURL: s.publicURL, Tool: tool, Token: token}
	v.Notice = "Viewer token " + name + " created. It is in the settings below and shown only now; revoke it under Users and API tokens."
	w.Header().Set("Cache-Control", "no-store")
	return render(w, r, IntegrationsPage(v))
}

// dashboard hands out a ready-made dashboard as a download.
func (s *Server) dashboard(w http.ResponseWriter, r *http.Request, _ auth.Principal) error {
	var b []byte
	switch r.PathValue("file") {
	case "grafana.json":
		b = deploy.GrafanaDashboard
	case "signoz.json":
		b = deploy.SigNozDashboard
	default:
		http.NotFound(w, r)
		return nil
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Disposition", `attachment; filename="goliash-`+r.PathValue("file")+`"`)
	_, err := w.Write(b)
	return err
}

// scrapeTarget splits the server's URL into what a Prometheus scrape config wants.
func scrapeTarget(serverURL string) (scheme, host, path string) {
	u, err := url.Parse(serverURL)
	if err != nil || u.Host == "" {
		return "https", "goliash.example.com", "/metrics"
	}
	return u.Scheme, u.Host, u.Path + "/metrics"
}

// tokenOr is the token when there is one, else a placeholder.
func tokenOr(token string) string {
	if token == "" {
		return "glsh_api_…"
	}
	return token
}

func metricsPathLine(path string) string {
	if path == "/metrics" {
		return ""
	}
	return fmt.Sprintf("\n    metrics_path: %s", path)
}

func prometheusConfig(serverURL string) string {
	scheme, host, path := scrapeTarget(serverURL)
	return fmt.Sprintf(`scrape_configs:
  - job_name: goliash
    scrape_interval: 60s
    scheme: %s%s
    authorization:
      credentials_file: /etc/prometheus/goliash-token
    static_configs:
      - targets: [%s]`, scheme, metricsPathLine(path), host)
}

func collectorConfig(serverURL string) string {
	scheme, host, path := scrapeTarget(serverURL)
	return fmt.Sprintf(`receivers:
  prometheus/goliash:
    config:
      scrape_configs:
        - job_name: goliash
          scrape_interval: 60s
          scheme: %s%s
          authorization:
            credentials_file: /etc/otelcol/goliash-token
          static_configs:
            - targets: [%s]

service:
  pipelines:
    metrics/goliash:
      receivers: [prometheus/goliash]
      processors: [batch]
      exporters: [otlp]   # the exporter that already sends to SigNoz`, scheme, indent(metricsPathLine(path), "      "), host)
}

func indent(s, by string) string {
	if s == "" {
		return ""
	}
	return "\n" + by + s[1:]
}

func mcpCommand(serverURL, token string) string {
	return fmt.Sprintf(`claude mcp add --transport http goliash %s/mcp \
  --header "Authorization: Bearer %s"`, serverURL, tokenOr(token))
}

func checkCommand(serverURL, token string) string {
	return fmt.Sprintf(`curl -s -H "Authorization: Bearer %s" %s/metrics | grep -c '^goliash_'`, tokenOr(token), serverURL)
}
