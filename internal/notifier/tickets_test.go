// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package notifier

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pipozzz/goliash/internal/store"
)

// jiraRecorder is a Jira that records what Goliash asks of it.
type jiraRecorder struct {
	srv      *httptest.Server
	mu       sync.Mutex
	issues   []map[string]any
	comments []string
	closed   []string
}

func newJiraRecorder(t *testing.T) *jiraRecorder {
	j := &jiraRecorder{}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /rest/api/2/issue", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Fields map[string]any `json:"fields"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		j.mu.Lock()
		j.issues = append(j.issues, in.Fields)
		j.mu.Unlock()
		_, _ = w.Write([]byte(`{"id":"1","key":"OPS-1"}`))
	})
	mux.HandleFunc("POST /rest/api/2/issue/{key}/comment", func(_ http.ResponseWriter, r *http.Request) {
		var in struct {
			Body string `json:"body"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		j.mu.Lock()
		j.comments = append(j.comments, in.Body)
		j.mu.Unlock()
	})
	mux.HandleFunc("GET /rest/api/2/issue/{key}/transitions", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"transitions":[{"id":"31","to":{"name":"Done","statusCategory":{"key":"done"}}}]}`))
	})
	mux.HandleFunc("POST /rest/api/2/issue/{key}/transitions", func(_ http.ResponseWriter, r *http.Request) {
		j.mu.Lock()
		j.closed = append(j.closed, r.PathValue("key"))
		j.mu.Unlock()
	})
	j.srv = httptest.NewServer(mux)
	t.Cleanup(j.srv.Close)
	return j
}

// A rule sending remediation deadlines to Jira files one ticket per upgrade, comments when more comes up, and the
// ticket closes once the upgrade is no longer to do.
func TestTicketLifecycle(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	jira := newJiraRecorder(t)
	ch := e.channel("jira", "jira", map[string]any{"url": jira.srv.URL, "token": "t", "project": "OPS"})
	e.rule(ch, "instant", []string{EventDeadline}, Filter{})
	e.db.Owner = "team-data"
	_ = e.st.UpdateService(ctx, e.db)
	drift, err := e.st.OpenDrift(ctx, store.Drift{
		Scope: e.sc, ServiceID: e.db.ID, EnvironmentID: e.prod.ID, Kind: "eol",
		Detail: json.RawMessage(`{"running":"13.4","eol":"2026-10-20"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = e.st.SetRemediation(ctx, e.sc, store.Remediation{EOL: 3})

	e.clock = time.Date(2026, 10, 21, 9, 0, 0, 0, time.UTC) // two days before the deadline
	for range 2 {
		_ = e.n.CheckDeadlines(ctx)
		if err := e.n.DeliverDue(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if len(jira.issues) != 1 || !strings.Contains(jira.issues[0]["summary"].(string), "postgres @ prod") ||
		!strings.Contains(jira.issues[0]["description"].(string), "end of life on 2026-10-20") || jira.issues[0]["duedate"] != "2026-10-23" {
		t.Fatalf("filed: %v", jira.issues)
	}
	// The deadline passes: the same ticket hears of it.
	e.clock = time.Date(2026, 10, 24, 9, 0, 0, 0, time.UTC)
	_ = e.n.CheckDeadlines(ctx)
	_ = e.n.DeliverDue(ctx)
	if len(jira.issues) != 1 || len(jira.comments) != 1 || !strings.Contains(jira.comments[0], "passed") {
		t.Fatalf("second alert: %d issues, comments %v", len(jira.issues), jira.comments)
	}
	// By hand, the open one is returned.
	if tk, err := e.n.FileTicket(ctx, e.sc, ch.ID, e.db.ID, e.prod.ID, ""); err != nil || tk.ExternalKey != "OPS-1" || len(jira.issues) != 1 {
		t.Errorf("by hand: %+v %v", tk, err)
	}
	// Still to do: stays open.
	_ = e.n.CloseTickets(ctx)
	if len(jira.closed) != 0 {
		t.Fatal("closed while still to do")
	}
	// Upgraded: the drift resolves and the ticket closes, once.
	_ = e.st.ResolveDrift(ctx, e.sc, drift.ID)
	_ = e.n.CloseTickets(ctx)
	_ = e.n.CloseTickets(ctx)
	if len(jira.closed) != 1 || !strings.HasPrefix(jira.comments[len(jira.comments)-1], "Done: postgres") {
		t.Errorf("close: %v %v", jira.closed, jira.comments)
	}
	if open, _ := e.st.ListOpenTickets(ctx, e.sc); len(open) != 0 {
		t.Errorf("still open: %+v", open)
	}
	// Nothing to upgrade any more: no ticket by hand.
	if _, err := e.n.FileTicket(ctx, e.sc, ch.ID, e.db.ID, e.prod.ID, ""); err == nil {
		t.Error("a ticket for nothing")
	}
}
