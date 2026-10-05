// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

// Package mcpserver lets AI assistants ask Goliash what runs where, through the Model
// Context Protocol. Every tool calls the REST API with the caller's API token, so an
// assistant sees and may do exactly what the token allows.
package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/pipozzz/goliash/pkg/buildinfo"
)

// Client calls a Goliash server's REST API.
type Client struct {
	BaseURL string // e.g. https://goliash.example.com
	Token   string // glsh_api_…
	HTTP    *http.Client
}

func (c *Client) do(ctx context.Context, method, path string, q url.Values, body any) (any, error) {
	u := strings.TrimRight(c.BaseURL, "/") + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rd)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	hc := c.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 30 * time.Second}
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("goliash: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode >= 300 {
		var p struct{ Title, Detail string }
		_ = json.Unmarshal(b, &p)
		return nil, fmt.Errorf("goliash answered %d: %s %s", resp.StatusCode, p.Title, p.Detail)
	}
	var out any
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, fmt.Errorf("goliash: %w", err)
	}
	return out, nil
}

// Result wraps what a tool returns.
type Result struct {
	Data any `json:"data"`
}

type (
	matrixIn struct {
		Service     string `json:"service,omitempty" jsonschema:"only this service"`
		Environment string `json:"environment,omitempty" jsonschema:"only this environment, e.g. prod"`
		At          string `json:"at,omitempty" jsonschema:"a past time, e.g. 2026-09-12T14:00 (UTC); empty means now"`
	}
	driftsIn struct {
		Kind        string `json:"kind,omitempty" jsonschema:"env, upstream, inconsistent, declared or eol"`
		Environment string `json:"environment,omitempty"`
		Service     string `json:"service,omitempty"`
	}
	changesIn struct {
		Since       string `json:"since,omitempty" jsonschema:"how far back, e.g. 2h, 30m or 7d; default 24h"`
		Environment string `json:"environment,omitempty"`
		Service     string `json:"service,omitempty"`
	}
	inventoryIn struct {
		At          string `json:"at,omitempty" jsonschema:"a past time (UTC); empty means now"`
		Environment string `json:"environment,omitempty"`
		Service     string `json:"service,omitempty"`
	}
	noIn  struct{}
	ackIn struct {
		Service      string `json:"service" jsonschema:"the service to acknowledge"`
		Kind         string `json:"kind" jsonschema:"release (quiet new-release notifications) or drift"`
		Environment  string `json:"environment,omitempty" jsonschema:"only in this environment"`
		UntilVersion string `json:"until_version,omitempty" jsonschema:"quiet until this version runs"`
		Until        string `json:"until,omitempty" jsonschema:"quiet until this time, RFC 3339"`
	}
)

// filter keeps the items of a list whose fields equal the given values.
func filter(data any, fields map[string]string) any {
	list, ok := data.([]any)
	if !ok {
		return data
	}
	out := []any{}
	for _, item := range list {
		m, _ := item.(map[string]any)
		keep := true
		for k, v := range fields {
			if v != "" && fmt.Sprint(m[k]) != v {
				keep = false
			}
		}
		if keep {
			out = append(out, item)
		}
	}
	return out
}

// NewServer returns an MCP server whose tools call c.
func NewServer(c *Client) *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{Name: "goliash", Title: "Goliash", Version: buildinfo.Version}, &mcp.ServerOptions{
		Instructions: "Goliash knows what runs where, on which version, in which environment (Kubernetes, ECS, Nomad, " +
			"Docker, Compose), the newest upstream releases, and where versions drift apart. Use matrix for the overview, " +
			"service for one service, drifts for what needs attention, changes for what changed recently.",
	})
	readOnly := &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: new(bool)}

	mcp.AddTool(s, &mcp.Tool{
		Name: "matrix", Annotations: readOnly,
		Description: "The service × environment matrix: versions running per environment with replicas and targets, " +
			"open drift and the newest upstream version. Filter by service or environment; at shows a past time.",
	},
		func(ctx context.Context, _ *mcp.CallToolRequest, in matrixIn) (*mcp.CallToolResult, Result, error) {
			q := url.Values{}
			if in.At != "" {
				q.Set("at", in.At)
			}
			data, err := c.do(ctx, http.MethodGet, "/api/v1/matrix", q, nil)
			if err != nil {
				return nil, Result{}, err
			}
			m, _ := data.(map[string]any)
			if m != nil && in.Service != "" {
				m["services"] = filter(m["services"], map[string]string{"service": in.Service})
			}
			if m != nil && in.Environment != "" {
				if rows, ok := m["services"].([]any); ok {
					for _, r := range rows {
						if row, ok := r.(map[string]any); ok {
							row["cells"] = filter(row["cells"], map[string]string{"environment": in.Environment})
						}
					}
				}
			}
			return nil, Result{Data: data}, nil
		})

	mcp.AddTool(s, &mcp.Tool{
		Name: "service", Annotations: readOnly,
		Description: "One service: where it runs and on which version, its newest upstream release, and its open drift.",
	},
		func(ctx context.Context, _ *mcp.CallToolRequest, in struct {
			Name string `json:"name" jsonschema:"the service name"`
		},
		) (*mcp.CallToolResult, Result, error) {
			data, err := c.do(ctx, http.MethodGet, "/api/v1/matrix", nil, nil)
			if err != nil {
				return nil, Result{}, err
			}
			m, _ := data.(map[string]any)
			rows := filter(m["services"], map[string]string{"service": in.Name})
			if list, _ := rows.([]any); len(list) == 0 {
				return nil, Result{}, fmt.Errorf("no service %q; call matrix for the list", in.Name)
			}
			return nil, Result{Data: rows.([]any)[0]}, nil
		})

	mcp.AddTool(s, &mcp.Tool{
		Name: "drifts", Annotations: readOnly,
		Description: "Open drift, oldest first: env (behind the previous environment), upstream (behind the newest " +
			"release), inconsistent (targets disagree), declared (differs from Compose files in Git), eol (release cycle " +
			"at its end of life).",
	},
		func(ctx context.Context, _ *mcp.CallToolRequest, in driftsIn) (*mcp.CallToolResult, Result, error) {
			data, err := c.do(ctx, http.MethodGet, "/api/v1/drifts", nil, nil)
			if err != nil {
				return nil, Result{}, err
			}
			return nil, Result{Data: filter(data, map[string]string{"kind": in.Kind, "environment": in.Environment, "service": in.Service})}, nil
		})

	mcp.AddTool(s, &mcp.Tool{
		Name: "changes", Annotations: readOnly,
		Description: "What changed recently, newest first: deploys, version changes, removals, new releases and drift. " +
			"Use it when something broke: \"what changed in prod in the last 2 hours?\"",
	},
		func(ctx context.Context, _ *mcp.CallToolRequest, in changesIn) (*mcp.CallToolResult, Result, error) {
			q := url.Values{"since": {"24h"}, "limit": {"200"}}
			if in.Since != "" {
				q.Set("since", in.Since)
			}
			if in.Environment != "" {
				q.Set("environment", in.Environment)
			}
			if in.Service != "" {
				q.Set("service", in.Service)
			}
			data, err := c.do(ctx, http.MethodGet, "/api/v1/events", q, nil)
			return nil, Result{Data: data}, err
		})

	for _, t := range []struct{ name, path, desc string }{
		{"promotions", "/api/v1/promotions", "Versions that run in one environment and wait for the next, longest waiting first, with the releases a promotion would bring."},
		{"delivery", "/api/v1/delivery", "Per service, versions that arrived in each environment in the last 30 days and the median lead time between environments."},
		{"hygiene", "/api/v1/hygiene", "Image hygiene: moving tags like latest, tags pushed again, untrusted registries, missing digests."},
	} {
		mcp.AddTool(s, &mcp.Tool{Name: t.name, Description: t.desc, Annotations: readOnly},
			func(ctx context.Context, _ *mcp.CallToolRequest, _ noIn) (*mcp.CallToolResult, Result, error) {
				data, err := c.do(ctx, http.MethodGet, t.path, nil, nil)
				return nil, Result{Data: data}, err
			})
	}

	mcp.AddTool(s, &mcp.Tool{
		Name: "inventory", Annotations: readOnly,
		Description: "Every running container (sidecars included) with image, tag and digest, now or at a past time.",
	},
		func(ctx context.Context, _ *mcp.CallToolRequest, in inventoryIn) (*mcp.CallToolResult, Result, error) {
			q := url.Values{}
			if in.At != "" {
				q.Set("at", in.At)
			}
			data, err := c.do(ctx, http.MethodGet, "/api/v1/inventory", q, nil)
			if err != nil {
				return nil, Result{}, err
			}
			return nil, Result{Data: filter(data, map[string]string{"environment": in.Environment, "service": in.Service})}, nil
		})

	no := false
	mcp.AddTool(s, &mcp.Tool{
		Name:        "acknowledge",
		Annotations: &mcp.ToolAnnotations{Title: "Acknowledge a release or drift", DestructiveHint: &no, OpenWorldHint: &no},
		Description: "Quiet notifications for a service (optionally in one environment) until a version runs or a time " +
			"passes. Needs a token with the member role. Confirm with the user before calling.",
	},
		func(ctx context.Context, _ *mcp.CallToolRequest, in ackIn) (*mcp.CallToolResult, Result, error) {
			body := map[string]any{"service": in.Service, "kind": in.Kind}
			for k, v := range map[string]string{"environment": in.Environment, "until_version": in.UntilVersion, "until": in.Until} {
				if v != "" {
					body[k] = v
				}
			}
			data, err := c.do(ctx, http.MethodPost, "/api/v1/acks", nil, body)
			return nil, Result{Data: data}, err
		})
	return s
}

// HTTPHandler serves MCP over Streamable HTTP. Every session uses the caller's bearer
// token against the REST API at apiURL (the server itself), so it can do what the
// token can.
func HTTPHandler(apiURL string) http.Handler {
	h := mcp.NewStreamableHTTPHandler(func(r *http.Request) *mcp.Server {
		token, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		return NewServer(&Client{BaseURL: apiURL, Token: token})
	}, &mcp.StreamableHTTPOptions{
		// No session kept in this server's memory: every request stands alone, so any
		// server of a cluster can answer it (the tools need no state between calls).
		Stateless: true,
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer glsh_api_") {
			w.Header().Set("WWW-Authenticate", `Bearer realm="goliash"`)
			http.Error(w, "send an API token: Authorization: Bearer glsh_api_…", http.StatusUnauthorized)
			return
		}
		h.ServeHTTP(w, r)
	})
}
