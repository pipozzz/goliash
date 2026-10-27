// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package tickets

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestGitLabIssues(t *testing.T) {
	rec := &recorder{}
	srv := rec.server(t, map[string]string{
		"POST /api/v4/projects/acme/payments/issues":         `{"iid":5,"web_url":"https://gitlab.example/acme/payments/-/issues/5"}`,
		"POST /api/v4/projects/acme/payments/issues/5/notes": `{}`,
		"PUT /api/v4/projects/acme/payments/issues/5":        `{}`,
		"GET /api/v4/projects/acme/platform":                 `{}`,
	})
	gl, ctx := GitLab{HTTP: http.DefaultClient}, context.Background()
	cfg := json.RawMessage(`{"url":"` + srv.URL + `","token":"glpat","project":"acme/platform","teams":"team-payments=acme/payments"}`)
	ref, err := gl.Create(ctx, cfg, Draft{Title: "t", Body: "b", Priority: 2, Team: "team-payments", Due: time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC)})
	if err != nil || ref.Key != "acme/payments#5" {
		t.Fatalf("create: %+v %v", ref, err)
	}
	if issue := rec.find("POST /api/v4/projects/acme/payments/issues "); !strings.Contains(issue, `"due_date":"2026-11-10"`) || !strings.Contains(issue, `priority::medium`) {
		t.Errorf("issue: %s", issue)
	}
	if err := gl.Close(ctx, cfg, ref, "Done"); err != nil {
		t.Fatal(err)
	}
	if p := rec.find("PUT /api/v4/projects/acme/payments/issues/5"); !strings.Contains(p, `"state_event":"close"`) {
		t.Errorf("close: %s", p)
	}
	if err := gl.Check(ctx, json.RawMessage(`{"url":"`+srv.URL+`","token":"t","project":"acme/platform"}`)); err != nil {
		t.Errorf("check: %v", err)
	}
}

// fakeLinear answers Linear's GraphQL by the operation in the query.
func fakeLinear(t *testing.T) (*httptest.Server, *[]string) {
	var mu sync.Mutex
	var got []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		q := string(b)
		mu.Lock()
		got = append(got, r.Header.Get("Authorization")+" "+q)
		mu.Unlock()
		switch {
		case strings.Contains(q, "teams(first"):
			_, _ = w.Write([]byte(`{"data":{"teams":{"nodes":[{"id":"team-1","key":"PAY","name":"Payments"}]}}}`))
		case strings.Contains(q, "issueCreate"):
			_, _ = w.Write([]byte(`{"data":{"issueCreate":{"success":true,"issue":{"id":"iss-1","identifier":"PAY-3","url":"https://linear.app/acme/issue/PAY-3"}}}}`))
		case strings.Contains(q, "states"):
			_, _ = w.Write([]byte(`{"data":{"issue":{"team":{"states":{"nodes":[{"id":"s1","type":"started"},{"id":"s2","type":"completed"}]}}}}}`))
		default:
			_, _ = w.Write([]byte(`{"data":{"ok":{"success":true}}}`))
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &got
}

func TestLinear(t *testing.T) {
	srv, got := fakeLinear(t)
	l, ctx := Linear{HTTP: http.DefaultClient, API: srv.URL}, context.Background()
	cfg := json.RawMessage(`{"token":"lin_api_x","team":"PAY"}`)
	ref, err := l.Create(ctx, cfg, Draft{Title: "t", Body: "b", Priority: 0, Due: time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC)})
	if err != nil || ref.Key != "PAY-3" || ref.ID != "iss-1" {
		t.Fatalf("create: %+v %v", ref, err)
	}
	create := ""
	for _, g := range *got {
		if strings.Contains(g, "issueCreate") {
			create = g
		}
	}
	if !strings.HasPrefix(create, "lin_api_x ") || !strings.Contains(create, `"priority":1`) || !strings.Contains(create, `"teamId":"team-1"`) || !strings.Contains(create, `"dueDate":"2026-11-10"`) {
		t.Errorf("create: %s", create)
	}
	if err := l.Close(ctx, cfg, ref, "Done"); err != nil {
		t.Fatal(err)
	}
	if last := (*got)[len(*got)-1]; !strings.Contains(last, "issueUpdate") || !strings.Contains(last, `"stateId":"s2"`) {
		t.Errorf("close: %s", last)
	}
	if err := l.Check(ctx, json.RawMessage(`{"token":"k","team":"NOPE"}`)); err == nil {
		t.Error("an unknown team passed")
	}
}
