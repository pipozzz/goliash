// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package notifier

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

	"github.com/pipozzz/goliash/internal/store"
)

type recorded struct {
	mu   sync.Mutex
	reqs []struct {
		path, auth string
		body       map[string]any
	}
}

func (r *recorded) server(t *testing.T) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		raw, _ := io.ReadAll(req.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		r.mu.Lock()
		r.reqs = append(r.reqs, struct {
			path, auth string
			body       map[string]any
		}{req.URL.RequestURI(), req.Header.Get("Authorization"), body})
		r.mu.Unlock()
		w.WriteHeader(http.StatusAccepted)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func oncallMessage(items ...Item) Message {
	return Message{Workspace: "Default", Link: "https://goliash.example.com", Items: items}
}

// A silent agent opens an incident, its repeat joins it, and its return resolves it; a vulnerability in production
// is critical and links back to Goliash and to its advisory.
func TestPagerDuty(t *testing.T) {
	var rec recorded
	srv := rec.server(t)
	pd := PagerDuty{HTTP: srv.Client(), URL: srv.URL + "/v2/enqueue"}
	ch := store.Channel{Name: "oncall", Config: []byte(`{"token":"rk"}`)}
	at := time.Date(2026, 11, 9, 10, 0, 0, 0, time.UTC)
	stale := Item{Type: "agent_stale", Target: "eu-cluster", At: at, Text: "agent eu-cluster has been silent for 10 min"}
	vuln := Item{
		Type: "vulnerability", Service: "payments", Environment: "prod", Production: true, From: "2.14.1", To: "2.17.1",
		Note: "CVE-2021-44228", At: at, Text: "payments in prod: log4j-core CVE-2021-44228", URL: "https://osv.dev/CVE-2021-44228",
	}
	ctx := context.Background()
	for _, m := range []Message{oncallMessage(stale, vuln), oncallMessage(stale), oncallMessage(Item{Type: "agent_back", Target: "eu-cluster", At: at, Text: "back"})} {
		if err := pd.Send(ctx, ch, m); err != nil {
			t.Fatal(err)
		}
	}
	if len(rec.reqs) != 4 {
		t.Fatalf("%d events", len(rec.reqs))
	}
	first, v, again, back := rec.reqs[0].body, rec.reqs[1].body, rec.reqs[2].body, rec.reqs[3].body
	if first["routing_key"] != "rk" || first["event_action"] != "trigger" || again["dedup_key"] != first["dedup_key"] {
		t.Fatalf("first %v again %v", first, again)
	}
	if back["event_action"] != "resolve" || back["dedup_key"] != first["dedup_key"] || back["payload"] != nil {
		t.Fatalf("resolve %v", back)
	}
	p := v["payload"].(map[string]any)
	if p["severity"] != "critical" || p["component"] != "payments" || p["group"] != "prod" || !strings.Contains(p["summary"].(string), "CVE-2021-44228") {
		t.Fatalf("vulnerability payload %v", p)
	}
	links := v["links"].([]any)
	if len(links) != 2 || links[0].(map[string]any)["href"] != "https://goliash.example.com/?q=payments" {
		t.Fatalf("links %v", links)
	}
	// Reports are not paged.
	rec.reqs = nil
	if err := pd.Send(ctx, ch, oncallMessage(Item{Type: "posture", Text: "posture"})); err != nil || len(rec.reqs) != 0 {
		t.Fatalf("a report was paged: %v %d", err, len(rec.reqs))
	}
	if err := pd.Send(ctx, store.Channel{Name: "x", Config: []byte(`{}`)}, oncallMessage(stale)); err == nil {
		t.Fatal("no key, no error")
	}
}

// Opsgenie gets an alert with an alias, and a drift resolved closes it by that alias.
func TestOpsgenie(t *testing.T) {
	var rec recorded
	srv := rec.server(t)
	og := Opsgenie{HTTP: srv.Client()}
	ch := store.Channel{Name: "oncall", Config: []byte(`{"token":"gk","url":"` + srv.URL + `/"}`)}
	drift := Item{Type: "drift_detected", Service: "web", Environment: "prod", Note: "env", Text: "web: prod runs 1.2, staging 1.3", At: time.Now()}
	ctx := context.Background()
	if err := og.Send(ctx, ch, oncallMessage(drift)); err != nil {
		t.Fatal(err)
	}
	resolved := drift
	resolved.Type, resolved.Text = "drift_resolved", "web: env drift resolved"
	if err := og.Send(ctx, ch, oncallMessage(resolved)); err != nil {
		t.Fatal(err)
	}
	open, closed := rec.reqs[0], rec.reqs[1]
	if open.path != "/v2/alerts" || open.auth != "GenieKey gk" || open.body["priority"] != "P3" || open.body["source"] != "Goliash" {
		t.Fatalf("open %+v", open)
	}
	alias := open.body["alias"].(string)
	if !strings.HasPrefix(closed.path, "/v2/alerts/") || !strings.HasSuffix(closed.path, "/close?identifierType=alias") ||
		!strings.Contains(closed.path, "drift_detected") {
		t.Fatalf("close %s (alias %s)", closed.path, alias)
	}
}
