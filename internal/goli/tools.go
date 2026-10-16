// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package goli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/pipozzz/goliash/internal/mcpserver"
	"github.com/pipozzz/goliash/pkg/buildinfo"
)

// readOnly are the MCP tools Goli may call: every tool that only reads. Acknowledging a drift changes
// Goliash, and Goli never does.
var readOnly = map[string]bool{
	"matrix": true, "service": true, "drifts": true, "updates": true, "changes": true, "vulnerabilities": true,
	"status": true, "inventory": true, "hygiene": true, "delivery": true, "promotions": true, "release_notes": true, "posture": true,
	"notifications": true, "images": true,
}

// MCPTools are the tools of Goliash's MCP server, run in this process against the REST API as the person
// whose request asked: the API sees their session, so Goli sees what they may see.
type MCPTools struct {
	session *mcp.ClientSession
}

// NewMCPTools connects to an in-process MCP server whose API calls api serves with the cookies of from.
// Close it when the question is answered.
func NewMCPTools(ctx context.Context, api http.Handler, from *http.Request) (*MCPTools, error) {
	// A browser asks with its session cookie; a script with its API token.
	in := inProcess{api: api, cookies: from.Cookies()}
	if a := from.Header.Get("Authorization"); strings.HasPrefix(a, "Bearer ") {
		in.auth = a
	}
	hc := &http.Client{Transport: in}
	server := mcpserver.NewServer(&mcpserver.Client{BaseURL: "http://goli.internal", HTTP: hc})
	st, ct := mcp.NewInMemoryTransports()
	if _, err := server.Connect(ctx, st, nil); err != nil {
		return nil, err
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "goli", Version: buildinfo.Version}, nil)
	cs, err := client.Connect(ctx, ct, nil)
	if err != nil {
		return nil, err
	}
	return &MCPTools{session: cs}, nil
}

// Close ends the in-process MCP session.
func (t *MCPTools) Close() error { return t.session.Close() }

// List returns the read-only tools.
func (t *MCPTools) List(ctx context.Context) ([]Tool, error) {
	res, err := t.session.ListTools(ctx, nil)
	if err != nil {
		return nil, err
	}
	var out []Tool
	for _, tool := range res.Tools {
		if !readOnly[tool.Name] {
			continue
		}
		var schema map[string]any
		b, err := json.Marshal(tool.InputSchema)
		if err != nil {
			return nil, err
		}
		if err := json.Unmarshal(b, &schema); err != nil {
			return nil, err
		}
		out = append(out, Tool{Name: tool.Name, Description: tool.Description, Schema: schema})
	}
	return out, nil
}

// Call runs a read-only tool and returns its text.
func (t *MCPTools) Call(ctx context.Context, name string, input json.RawMessage) (string, error) {
	if !readOnly[name] {
		return "", fmt.Errorf("goli cannot use %q", name)
	}
	var args map[string]any
	if len(input) > 0 {
		if err := json.Unmarshal(input, &args); err != nil {
			return "", fmt.Errorf("input: %w", err)
		}
	}
	res, err := t.session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		return "", err
	}
	var b strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	if res.IsError {
		return "", errors.New(b.String())
	}
	return b.String(), nil
}

// inProcess serves the MCP server's API calls with api, signed in with the asking person's cookies instead
// of a token. Only reads go through.
type inProcess struct {
	api     http.Handler
	cookies []*http.Cookie
	auth    string // the asking script's Authorization header
}

func (p inProcess) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.Method != http.MethodGet {
		return nil, fmt.Errorf("goli only reads: %s %s", r.Method, r.URL.Path)
	}
	r = r.Clone(r.Context())
	r.Header.Del("Authorization")
	if p.auth != "" {
		r.Header.Set("Authorization", p.auth)
	}
	for _, c := range p.cookies {
		r.AddCookie(c)
	}
	r.RequestURI = r.URL.RequestURI()
	w := httptest.NewRecorder()
	p.api.ServeHTTP(w, r)
	return w.Result(), nil
}
