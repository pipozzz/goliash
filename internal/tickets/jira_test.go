// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package tickets

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeJira is enough of Jira's REST API v2 for filing, commenting and closing.
type fakeJira struct {
	srv         *httptest.Server
	mu          sync.Mutex
	noPriority  bool // the project's screen lacks the priority field
	created     []map[string]any
	comments    []string
	transitions []string
	auth        string
}

func newFakeJira(t *testing.T) *fakeJira {
	f := &fakeJira{}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /rest/api/2/issue", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Fields map[string]any `json:"fields"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		f.mu.Lock()
		defer f.mu.Unlock()
		f.auth = r.Header.Get("Authorization")
		if _, ok := in.Fields["priority"]; ok && f.noPriority {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"errors":{"priority":"Field 'priority' cannot be set."}}`))
			return
		}
		f.created = append(f.created, in.Fields)
		_, _ = w.Write([]byte(`{"id":"10001","key":"PAY-7"}`))
	})
	mux.HandleFunc("POST /rest/api/2/issue/{key}/comment", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Body string `json:"body"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		f.mu.Lock()
		f.comments = append(f.comments, r.PathValue("key")+": "+in.Body)
		f.mu.Unlock()
		w.WriteHeader(http.StatusCreated)
	})
	mux.HandleFunc("GET /rest/api/2/issue/{key}/transitions", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"transitions":[
			{"id":"11","name":"Start","to":{"name":"In Progress","statusCategory":{"key":"indeterminate"}}},
			{"id":"21","name":"Won't do","to":{"name":"Rejected","statusCategory":{"key":"done"}}},
			{"id":"31","name":"Finish","to":{"name":"Done","statusCategory":{"key":"done"}}}]}`))
	})
	mux.HandleFunc("POST /rest/api/2/issue/{key}/transitions", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Transition struct {
				ID string `json:"id"`
			} `json:"transition"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		f.mu.Lock()
		f.transitions = append(f.transitions, r.PathValue("key")+" "+in.Transition.ID)
		f.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /rest/api/2/myself", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{}`))
	})
	mux.HandleFunc("GET /rest/api/2/project/{key}", func(w http.ResponseWriter, r *http.Request) {
		if r.PathValue("key") == "NOPE" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(`{}`))
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeJira) config(extra string) json.RawMessage {
	return json.RawMessage(`{"url":"` + f.srv.URL + `/","email":"bot@acme.io","token":"t0k","project":"OPS","teams":"team-payments=PAY"` + extra + `}`)
}

func TestJira(t *testing.T) {
	f := newFakeJira(t)
	j, ctx := Jira{HTTP: http.DefaultClient}, context.Background()
	f.noPriority = true // filed again without priority and due date
	ref, err := j.Create(ctx, f.config(`,"labels":"platform team"`), Draft{
		Title: "[Goliash] payments-api @ prod: upgrade 1.4.2 → 1.6.0", Body: "Why", Priority: 0,
		Due: time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC), Labels: []string{"goliash", "security"}, Team: "team-payments",
	})
	if err != nil || ref.Key != "PAY-7" || ref.URL != f.srv.URL+"/browse/PAY-7" {
		t.Fatalf("create: %+v %v", ref, err)
	}
	got := f.created[0]
	if got["project"].(map[string]any)["key"] != "PAY" || got["issuetype"].(map[string]any)["name"] != "Task" || got["priority"] != nil {
		t.Errorf("fields %v", got)
	}
	if labels, _ := json.Marshal(got["labels"]); string(labels) != `["goliash","security","platform-team"]` {
		t.Errorf("labels %s", labels)
	}
	if !strings.HasPrefix(f.auth, "Basic ") {
		t.Errorf("Cloud signs in with basic auth: %q", f.auth)
	}
	f.noPriority = false
	if _, err := j.Create(ctx, f.config(""), Draft{Title: "x", Priority: 1, Due: time.Now()}); err != nil || f.created[1]["priority"] == nil || f.created[1]["duedate"] == nil {
		t.Errorf("priority and due date: %v %v", f.created[1], err)
	}

	if err := j.Close(ctx, f.config(""), ref, "Done: payments-api runs 1.6.0 in prod."); err != nil {
		t.Fatal(err)
	}
	if len(f.comments) != 1 || !strings.Contains(f.comments[0], "runs 1.6.0") || len(f.transitions) != 1 || f.transitions[0] != "PAY-7 31" {
		t.Errorf("close: %v %v", f.comments, f.transitions)
	}
	if err := j.Check(ctx, f.config("")); err != nil {
		t.Errorf("check: %v", err)
	}
	if err := j.Check(ctx, json.RawMessage(`{"url":"`+f.srv.URL+`","token":"t","project":"NOPE"}`)); err == nil || !strings.Contains(err.Error(), "NOPE") {
		t.Errorf("a missing project: %v", err)
	}
}
