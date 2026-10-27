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

// recorder answers by path and records what came, method and path first.
type recorder struct {
	mu   sync.Mutex
	got  []string
	auth []string
}

func (rec *recorder) server(t *testing.T, answers map[string]string) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		key := r.Method + " " + r.URL.Path
		rec.mu.Lock()
		rec.got = append(rec.got, key+" "+string(body))
		rec.auth = append(rec.auth, r.Header.Get("Authorization")+r.Header.Get("Shortcut-Token"))
		rec.mu.Unlock()
		a, ok := answers[key]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(a))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func (rec *recorder) find(prefix string) string {
	for _, g := range rec.got {
		if strings.HasPrefix(g, prefix) {
			return g
		}
	}
	return ""
}

func TestShortcut(t *testing.T) {
	rec := &recorder{}
	srv := rec.server(t, map[string]string{
		"GET /groups":               `[{"id":"5f0c-1-2-3-4","mention_name":"payments","name":"Payments"}]`,
		"POST /stories":             `{"id":42,"app_url":"https://app.shortcut.com/acme/story/42"}`,
		"GET /stories/42":           `{"workflow_id":7}`,
		"GET /workflows/7":          `{"states":[{"id":1,"type":"unstarted"},{"id":3,"type":"done"}]}`,
		"PUT /stories/42":           `{}`,
		"GET /member":               `{}`,
		"POST /stories/42/comments": `{}`,
	})
	sc, ctx := Shortcut{HTTP: http.DefaultClient, Base: srv.URL}, context.Background()
	cfg := json.RawMessage(`{"token":"sc-tok","teams":"team-payments=payments"}`)
	ref, err := sc.Create(ctx, cfg, Draft{
		Title: "[Goliash] x", Body: "b", Priority: 0, Team: "team-payments", Labels: []string{"goliash"},
		Due: time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC),
	})
	if err != nil || ref.Key != "sc-42" || ref.URL != "https://app.shortcut.com/acme/story/42" {
		t.Fatalf("create: %+v %v", ref, err)
	}
	story := rec.find("POST /stories ")
	for _, want := range []string{`"group_id":"5f0c-1-2-3-4"`, `"story_type":"chore"`, `"deadline":"2026-11-10T00:00:00Z"`, `{"name":"priority-highest"}`} {
		if !strings.Contains(story, want) {
			t.Errorf("story lacks %s: %s", want, story)
		}
	}
	if rec.auth[0] != "sc-tok" {
		t.Errorf("token header: %q", rec.auth[0])
	}
	if err := sc.Close(ctx, cfg, ref, "Done"); err != nil {
		t.Fatal(err)
	}
	if put := rec.find("PUT /stories/42"); !strings.Contains(put, `"workflow_state_id":3`) || rec.find("POST /stories/42/comments") == "" {
		t.Errorf("close: %v", rec.got)
	}
	if err := sc.Check(ctx, json.RawMessage(`{"token":"t","team":"nobody"}`)); err == nil {
		t.Error("an unknown team passed the check")
	}
}

func TestGitHubIssues(t *testing.T) {
	rec := &recorder{}
	srv := rec.server(t, map[string]string{
		"POST /repos/acme/payments/issues":             `{"number":12,"html_url":"https://github.com/acme/payments/issues/12"}`,
		"POST /repos/acme/payments/issues/12/comments": `{}`,
		"PATCH /repos/acme/payments/issues/12":         `{}`,
		"GET /repos/acme/platform":                     `{"has_issues":true}`,
		"GET /repos/acme/quiet":                        `{"has_issues":false}`,
	})
	gh, ctx := GitHub{HTTP: http.DefaultClient}, context.Background()
	cfg := json.RawMessage(`{"url":"` + srv.URL + `","token":"ghp","repo":"acme/platform","teams":"team-payments=acme/payments"}`)
	ref, err := gh.Create(ctx, cfg, Draft{Title: "[Goliash] y", Body: "b", Priority: 1, Team: "team-payments", Labels: []string{"goliash", "security"}})
	if err != nil || ref.Key != "acme/payments#12" || ref.URL != "https://github.com/acme/payments/issues/12" {
		t.Fatalf("create: %+v %v", ref, err)
	}
	if issue := rec.find("POST /repos/acme/payments/issues "); !strings.Contains(issue, `"labels":["goliash","security","priority: high"]`) {
		t.Errorf("issue: %s", issue)
	}
	if rec.auth[0] != "Bearer ghp" {
		t.Errorf("auth: %q", rec.auth[0])
	}
	if err := gh.Close(ctx, cfg, ref, "Done"); err != nil {
		t.Fatal(err)
	}
	if p := rec.find("PATCH /repos/acme/payments/issues/12"); !strings.Contains(p, `"state":"closed"`) {
		t.Errorf("close: %s", p)
	}
	if err := gh.Check(ctx, cfg); err == nil { // acme/payments answers no repository
		t.Error("a missing repository passed")
	}
	if err := gh.Check(ctx, json.RawMessage(`{"url":"`+srv.URL+`","token":"t","repo":"acme/quiet"}`)); err == nil || !strings.Contains(err.Error(), "issues turned off") {
		t.Errorf("issues off: %v", err)
	}
}
