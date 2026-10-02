// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package notifier

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pipozzz/goliash/internal/store"
)

type sink struct {
	mu      sync.Mutex
	bodies  [][]byte
	headers []http.Header
	fail    bool
	srv     *httptest.Server
}

func newSink(t *testing.T) *sink {
	s := &sink{}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.fail {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		s.bodies = append(s.bodies, b)
		s.headers = append(s.headers, r.Header.Clone())
	}))
	t.Cleanup(s.srv.Close)
	return s
}

func (s *sink) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.bodies)
}

func (s *sink) last() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return string(s.bodies[len(s.bodies)-1])
}

type env struct {
	t     *testing.T
	st    *store.Store
	sc    store.Scope
	n     *Notifier
	clock time.Time
	svc   store.Service
	db    store.Service
	prod  store.Environment
	stg   store.Environment
}

func newEnv(t *testing.T) *env {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, "sqlite://:memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ws, _ := st.EnsureDefaultWorkspace(ctx)
	e := &env{t: t, st: st, sc: ws.Scope(), clock: time.Date(2026, 10, 2, 10, 0, 0, 0, time.UTC)}
	e.svc, _ = st.EnsureService(ctx, e.sc, "payments-api")
	e.svc.Owner = "team-pay"
	_ = st.UpdateService(ctx, e.svc)
	e.db, _ = st.EnsureService(ctx, e.sc, "postgres")
	e.stg, _ = st.CreateEnvironment(ctx, e.sc, "staging", 20)
	e.prod, _ = st.CreateEnvironment(ctx, e.sc, "prod", 30)
	e.n = New(st, slog.New(slog.NewTextHandler(io.Discard, nil)), DefaultSenders(http.DefaultClient, SMTPConfig{}))
	e.n.now = func() time.Time { return e.clock }
	return e
}

func (e *env) channel(typ, name string, cfg map[string]any) store.Channel {
	e.t.Helper()
	raw, _ := json.Marshal(cfg)
	ch, err := e.st.CreateChannel(context.Background(), store.Channel{Scope: e.sc, Type: typ, Name: name, Config: raw})
	if err != nil {
		e.t.Fatal(err)
	}
	return ch
}

func (e *env) rule(ch store.Channel, mode string, types []string, f Filter) {
	e.t.Helper()
	raw, _ := json.Marshal(f)
	if _, err := e.st.CreateRule(context.Background(), store.Rule{Scope: e.sc, ChannelID: ch.ID, EventTypes: types, Filter: raw, Mode: mode}); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) emit(evs ...store.Event) {
	e.t.Helper()
	for i := range evs {
		if evs[i].ID == "" {
			evs[i].ID = store.NewID()
		}
		if evs[i].At.IsZero() {
			evs[i].At = e.clock
		}
	}
	if err := e.n.handle(context.Background(), e.sc, evs); err != nil {
		e.t.Fatal(err)
	}
	if err := e.n.DeliverDue(context.Background()); err != nil {
		e.t.Fatal(err)
	}
}

func release(svc store.Service, from, to, jump string) store.Event {
	return store.Event{Type: "new_release", ServiceID: svc.ID, FromVersion: from, ToVersion: to, Note: jump}
}

func TestInstantSlackBatchDedupAndAck(t *testing.T) {
	e := newEnv(t)
	slack := newSink(t)
	e.rule(e.channel("slack", "ops", map[string]any{"url": slack.srv.URL}), "instant", []string{"new_release", "drift_detected"}, Filter{})

	e.emit(
		release(e.svc, "1.5.0", "1.6.0", "minor"),
		store.Event{Type: "drift_detected", ServiceID: e.svc.ID, EnvironmentID: e.prod.ID, FromVersion: "1.5.0", ToVersion: "1.6.0", Note: "env"},
		store.Event{Type: "deployed", ServiceID: e.svc.ID, ToVersion: "1.6.0"}, // not subscribed
	)
	if slack.count() != 1 {
		t.Fatalf("slack messages = %d, want one batched message", slack.count())
	}
	body := slack.last()
	if !strings.Contains(body, "new release 1.6.0 (minor), running 1.5.0") || !strings.Contains(body, "payments-api @ prod: runs 1.5.0, behind 1.6.0") || strings.Contains(body, "deployed") {
		t.Fatalf("slack body %s", body)
	}

	// The same release again is not announced twice.
	e.emit(release(e.svc, "1.5.0", "1.6.0", "minor"))
	if slack.count() != 1 {
		t.Fatal("duplicate release announced")
	}

	// "We know about 2.0, quiet until 2.1."
	if _, err := e.st.CreateAck(context.Background(), store.Ack{Scope: e.sc, ServiceID: e.svc.ID, Kind: "release", UntilVersion: "2.1.0"}); err != nil {
		t.Fatal(err)
	}
	e.emit(release(e.svc, "1.6.0", "2.0.0", "major"))
	if slack.count() != 1 {
		t.Fatal("acked release announced")
	}
	e.emit(release(e.svc, "1.6.0", "2.1.0", "major"))
	if slack.count() != 2 || !strings.Contains(slack.last(), "2.1.0") {
		t.Fatalf("release past the ack not announced: %d", slack.count())
	}

	// "Quiet for 14 days" on drift.
	_, _ = e.st.CreateAck(context.Background(), store.Ack{Scope: e.sc, ServiceID: e.svc.ID, Kind: "drift", UntilAt: e.clock.Add(14 * 24 * time.Hour)})
	e.emit(store.Event{Type: "drift_detected", ServiceID: e.svc.ID, EnvironmentID: e.stg.ID, FromVersion: "1.5.0", ToVersion: "2.1.0", Note: "upstream"})
	if slack.count() != 2 {
		t.Fatal("snoozed drift announced")
	}
}

func TestFilters(t *testing.T) {
	e := newEnv(t)
	slack := newSink(t)
	ch := e.channel("slack", "pay", map[string]any{"url": slack.srv.URL})
	e.rule(ch, "instant", nil, Filter{Owners: []string{"team-pay"}, Environments: []string{"prod"}, MinJump: "minor"})

	e.emit(release(e.svc, "1.5.0", "1.5.1", "patch")) // too small
	e.emit(release(e.db, "15.6", "15.7", "minor"))    // other owner
	e.emit(store.Event{Type: "version_changed", ServiceID: e.svc.ID, EnvironmentID: e.stg.ID, FromVersion: "1", ToVersion: "2"})
	if slack.count() != 0 {
		t.Fatalf("filtered events delivered: %s", slack.last())
	}
	e.emit(store.Event{Type: "version_changed", ServiceID: e.svc.ID, EnvironmentID: e.prod.ID, FromVersion: "1.5.0", ToVersion: "1.6.0"})
	e.emit(release(e.svc, "1.5.0", "1.6.0", "minor"))
	if slack.count() != 2 {
		t.Fatalf("matching events: %d", slack.count())
	}
}

func TestDailyDigest(t *testing.T) {
	e := newEnv(t)
	slack := newSink(t)
	hour := 7
	e.rule(e.channel("slack", "digest", map[string]any{"url": slack.srv.URL}), "daily", nil, Filter{DigestHour: &hour})

	e.emit(release(e.svc, "1.5.0", "1.6.0", "minor"), release(e.db, "15.6", "15.7", "minor"))
	if slack.count() != 0 {
		t.Fatal("digest sent before its hour")
	}
	e.clock = time.Date(2026, 10, 3, 7, 0, 1, 0, time.UTC)
	_ = e.n.DeliverDue(context.Background())
	if slack.count() != 1 || !strings.Contains(slack.last(), "2 updates") {
		t.Fatalf("digest: %d %s", slack.count(), slack.last())
	}
	_ = e.n.DeliverDue(context.Background())
	if slack.count() != 1 {
		t.Fatal("digest sent twice")
	}

	if got := dueAt("weekly", Filter{}, time.Date(2026, 10, 2, 10, 0, 0, 0, time.UTC)); got != time.Date(2026, 10, 5, 8, 0, 0, 0, time.UTC) {
		t.Fatalf("weekly due %v (2026-10-05 is a Monday)", got)
	}
}

func TestWebhookSignedAndRetried(t *testing.T) {
	e := newEnv(t)
	hook := newSink(t)
	e.rule(e.channel("webhook", "ci", map[string]any{"url": hook.srv.URL, "secret": "s3cret"}), "instant", nil, Filter{})

	hook.fail = true
	e.emit(release(e.svc, "1.5.0", "1.6.0", "minor"))
	items, _ := e.st.DueItems(context.Background(), e.clock.Add(time.Hour), 10)
	if len(items) != 1 || items[0].Attempts != 1 || !strings.Contains(items[0].LastError, "500") {
		t.Fatalf("failed delivery not recorded for retry: %+v", items)
	}

	hook.mu.Lock()
	hook.fail = false
	hook.mu.Unlock()
	_ = e.n.DeliverDue(context.Background()) // retry not due yet
	if hook.count() != 0 {
		t.Fatal("retried before backoff")
	}
	e.clock = e.clock.Add(2 * time.Minute)
	_ = e.n.DeliverDue(context.Background())
	if hook.count() != 1 {
		t.Fatal("not retried after backoff")
	}

	h := hook.headers[0]
	body := []byte(hook.last())
	if h.Get("X-Goliash-Signature") != "sha256="+Sign("s3cret", h.Get("X-Goliash-Timestamp"), body) {
		t.Fatal("bad signature")
	}
	var msg Message
	if err := json.Unmarshal(body, &msg); err != nil || msg.Items[0].Service != "payments-api" || msg.Items[0].To != "1.6.0" {
		t.Fatalf("payload %s", body)
	}
}

func TestAgentStale(t *testing.T) {
	e := newEnv(t)
	slack := newSink(t)
	e.rule(e.channel("slack", "ops", map[string]any{"url": slack.srv.URL}), "instant", []string{"agent_stale"}, Filter{Services: []string{"x"}})
	e.n.AgentStale(store.Agent{ID: "a1", Name: "eu-cluster", Scope: e.sc, StaleSince: e.clock})
	_ = e.n.DeliverDue(context.Background())
	if slack.count() != 1 || !strings.Contains(slack.last(), "agent eu-cluster has not sent a heartbeat") {
		t.Fatalf("stale agent: %d", slack.count())
	}
}

// fakeSMTP accepts one message and records it.
func fakeSMTP(t *testing.T) (addr string, got chan string) {
	ln, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	got = make(chan string, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		r := bufio.NewReader(conn)
		say := func(s string) { _, _ = conn.Write([]byte(s + "\r\n")) }
		say("220 fake")
		var data strings.Builder
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			cmd := strings.ToUpper(strings.TrimSpace(line))
			switch {
			case strings.HasPrefix(cmd, "EHLO"), strings.HasPrefix(cmd, "HELO"):
				say("250 fake")
			case strings.HasPrefix(cmd, "DATA"):
				say("354 go")
				for {
					l, err := r.ReadString('\n')
					if err != nil || l == ".\r\n" {
						break
					}
					data.WriteString(l)
				}
				say("250 ok")
				got <- data.String()
			case strings.HasPrefix(cmd, "QUIT"):
				say("221 bye")
				return
			default:
				say("250 ok")
			}
		}
	}()
	return ln.Addr().String(), got
}

func TestEmail(t *testing.T) {
	e := newEnv(t)
	addr, got := fakeSMTP(t)
	e.n.senders["email"] = Email{Config: SMTPConfig{Addr: addr, From: "goliash@example.com"}}
	e.rule(e.channel("email", "oncall", map[string]any{"to": []string{"oncall@example.com"}}), "instant", nil, Filter{})
	e.emit(release(e.svc, "1.5.0", "1.6.0", "minor"))
	select {
	case mail := <-got:
		if !strings.Contains(mail, "To: oncall@example.com") || !strings.Contains(mail, "Subject: payments-api: new release 1.6.0") {
			t.Fatalf("mail %s", mail)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no mail")
	}

	if err := (Email{}).Send(context.Background(), store.Channel{}, Message{}); !errors.Is(err, ErrNoSMTP) {
		t.Fatalf("unconfigured smtp: %v", err)
	}
}
