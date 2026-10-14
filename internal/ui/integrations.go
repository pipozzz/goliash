// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package ui

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"

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
	Envs      []string // the workspace's environments, highest last
}

// integrationTools are the tools a token can be made for here, with the token's name.
var integrationTools = map[string]string{"prometheus": "prometheus", "signoz": "signoz", "mcp": "assistant", "dtrack": "dependency-track", "widget": "widget"}

func (s *Server) integrations(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	v := IntegrationsView{Base: withFlash(s.base(r.Context(), p, "integrations", "Integrations"), r), ServerURL: s.publicURL}
	v.Envs = s.envNames(r.Context(), p.Scope)
	return render(w, r, IntegrationsPage(v))
}

// envNames lists the workspace's environment names in their order.
func (s *Server) envNames(ctx context.Context, sc store.Scope) []string {
	envs, err := s.store.ListEnvironments(ctx, sc)
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(envs))
	for _, e := range envs {
		names = append(names, e.Name)
	}
	return names
}

// dependencyTrackScript uploads what runs in each environment to Dependency-Track as a project of its own,
// for cron or a CI schedule. token is filled in when it was just made.
func dependencyTrackScript(serverURL, token string, envs []string) string {
	if token == "" {
		token = "glsh_api_…" //nolint:gosec // a placeholder for the reader to replace
	}
	if len(envs) == 0 {
		envs = []string{"production"}
	}
	var b strings.Builder
	b.WriteString("#!/bin/sh\n# Run it every hour, e.g. from cron: 0 * * * * /usr/local/bin/goliash-to-dtrack\nset -eu\n")
	fmt.Fprintf(&b, "GOLIASH=%s\nGOLIASH_TOKEN=%s\n", shellQuote(serverURL), shellQuote(token))
	b.WriteString("DTRACK=https://dtrack.example.com   # Dependency-Track's API URL\n")
	b.WriteString("DTRACK_API_KEY=odt_…                # a team API key with BOM_UPLOAD and PROJECT_CREATION_UPLOAD (401: one is missing)\n")
	b.WriteString("tmp=$(mktemp)\ntrap 'rm -f \"$tmp\"' EXIT\n")
	for _, env := range envs {
		fmt.Fprintf(&b, "\ncurl -fsS -H \"Authorization: Bearer $GOLIASH_TOKEN\" -o \"$tmp\" \\\n  \"$GOLIASH/api/v1/inventory?format=cyclonedx&environment=%s\"\n", url.QueryEscape(env))
		fmt.Fprintf(&b, "curl -fsS -X POST \"$DTRACK/api/v1/bom\" -H \"X-Api-Key: $DTRACK_API_KEY\" -F autoCreate=true \\\n  -F projectName=%s -F projectVersion=running -F bom=@\"$tmp\" >/dev/null\n", shellQuote("goliash "+env))
	}
	return b.String()
}

// shellQuote quotes s for a POSIX shell.
func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

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
	v.Envs = s.envNames(r.Context(), p.Scope)
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
	case "alerts.yaml":
		b = deploy.PrometheusAlerts
	default:
		http.NotFound(w, r)
		return nil
	}
	w.Header().Set("Content-Type", "application/json")
	if strings.HasSuffix(r.PathValue("file"), ".yaml") {
		w.Header().Set("Content-Type", "application/yaml")
	}
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

// mcpOAuthCommand adds Goliash to Claude Code with no token: it signs in with OAuth on first use.
func mcpOAuthCommand(serverURL string) string {
	return fmt.Sprintf(`claude mcp add --transport http goliash %s/mcp`, serverURL)
}

func mcpCommand(serverURL, token string) string {
	return fmt.Sprintf(`claude mcp add --transport http goliash %s/mcp \
  --header "Authorization: Bearer %s"`, serverURL, tokenOr(token))
}

// summaryCommand reads what the widget shows, to check its token.
func summaryCommand(serverURL, token string) string {
	return fmt.Sprintf(`curl -s -H "Authorization: Bearer %s" %s/api/v1/summary`, tokenOr(token), serverURL)
}

func checkCommand(serverURL, token string) string {
	return fmt.Sprintf(`curl -s -H "Authorization: Bearer %s" %s/metrics | grep -c '^goliash_'`, tokenOr(token), serverURL)
}
