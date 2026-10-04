// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package mcpserver

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type fakeAPI struct {
	mu      sync.Mutex
	queries map[string]string
	ackBody string
}

func (f *fakeAPI) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer glsh_api_test" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"title":"Unauthorized","detail":"send a token"}`))
			return
		}
		f.mu.Lock()
		f.queries[r.URL.Path] = r.URL.RawQuery
		f.mu.Unlock()
		switch r.URL.Path {
		case "/api/v1/matrix":
			_, _ = w.Write([]byte(`{"environments":[{"name":"staging"},{"name":"prod"}],"services":[
				{"service":"web","cells":[{"environment":"staging","versions":[{"tag":"1.28.0"}]},{"environment":"prod","versions":[{"tag":"1.27.2"}]}]},
				{"service":"api","cells":[]}],"unmapped":0}`))
		case "/api/v1/drifts":
			_, _ = w.Write([]byte(`[{"service":"web","environment":"prod","kind":"env"},{"service":"web","environment":"prod","kind":"eol"}]`))
		case "/api/v1/events":
			_, _ = w.Write([]byte(`[{"type":"version_changed","service":"web"}]`))
		case "/api/v1/acks":
			b, _ := io.ReadAll(r.Body)
			f.mu.Lock()
			f.ackBody = string(b)
			f.mu.Unlock()
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":"ack1"}`))
		default:
			_, _ = w.Write([]byte(`[]`))
		}
	})
}

func connect(t *testing.T, apiURL, token string) *mcp.ClientSession {
	t.Helper()
	ctx := context.Background()
	st, ct := mcp.NewInMemoryTransports()
	if _, err := NewServer(&Client{BaseURL: apiURL, Token: token}).Connect(ctx, st, nil); err != nil {
		t.Fatal(err)
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

func call(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any) (map[string]any, bool) {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	var out map[string]any
	b, _ := json.Marshal(res.StructuredContent)
	_ = json.Unmarshal(b, &out)
	return out, res.IsError
}

func TestTools(t *testing.T) {
	f := &fakeAPI{queries: map[string]string{}}
	srv := httptest.NewServer(f.handler())
	defer srv.Close()
	cs := connect(t, srv.URL, "glsh_api_test")

	tools, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tl := range tools.Tools {
		names = append(names, tl.Name)
		if tl.Name != "acknowledge" && !tl.Annotations.ReadOnlyHint {
			t.Errorf("%s must be read-only", tl.Name)
		}
	}
	if len(names) != 9 {
		t.Fatalf("tools %v", names)
	}

	out, _ := call(t, cs, "matrix", map[string]any{"service": "web", "environment": "prod"})
	b, _ := json.Marshal(out)
	if !strings.Contains(string(b), `"1.27.2"`) || strings.Contains(string(b), `"1.28.0"`) || strings.Contains(string(b), `"api"`) {
		t.Fatalf("matrix filter: %s", b)
	}
	out, _ = call(t, cs, "drifts", map[string]any{"kind": "eol"})
	if list := out["data"].([]any); len(list) != 1 {
		t.Fatalf("drift filter: %v", out)
	}
	call(t, cs, "changes", map[string]any{"since": "2h", "environment": "prod"})
	if q := f.queries["/api/v1/events"]; !strings.Contains(q, "since=2h") || !strings.Contains(q, "environment=prod") {
		t.Fatalf("changes query %q", q)
	}
	if _, isErr := call(t, cs, "service", map[string]any{"name": "missing"}); !isErr {
		t.Fatal("unknown service is not an error")
	}
	call(t, cs, "acknowledge", map[string]any{"service": "web", "kind": "release", "until_version": "1.29.0"})
	if !strings.Contains(f.ackBody, `"until_version":"1.29.0"`) || strings.Contains(f.ackBody, "environment") {
		t.Fatalf("ack body %s", f.ackBody)
	}

	bad := connect(t, srv.URL, "glsh_api_wrong")
	res, err := bad.CallTool(context.Background(), &mcp.CallToolParams{Name: "matrix", Arguments: map[string]any{}})
	if err != nil || !res.IsError || !strings.Contains(res.Content[0].(*mcp.TextContent).Text, "401") {
		t.Fatalf("a wrong token must surface as a tool error: %+v %v", res, err)
	}
}

func TestHTTPHandlerNeedsAToken(t *testing.T) {
	srv := httptest.NewServer(HTTPHandler("http://127.0.0.1:1"))
	defer srv.Close()
	resp, err := http.Post(srv.URL, "application/json", strings.NewReader(`{}`)) //nolint:noctx // test
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized || resp.Header.Get("WWW-Authenticate") == "" {
		t.Fatalf("status %d", resp.StatusCode)
	}
}
