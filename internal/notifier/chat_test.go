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
	"testing"
	"time"

	"github.com/pipozzz/goliash/internal/store"
)

type captured struct {
	path   string
	header http.Header
	body   string
}

func capture(t *testing.T, status int) (*httptest.Server, *captured) {
	c := &captured{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		c.path, c.header, c.body = r.URL.Path, r.Header.Clone(), string(b)
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"ok":false,"description":"Bad Request: chat not found"}`))
	}))
	t.Cleanup(srv.Close)
	return srv, c
}

func channel(typ string, cfg string) store.Channel {
	return store.Channel{Type: typ, Name: "ops", Config: json.RawMessage(cfg)}
}

var newRelease = Message{Workspace: "Default", Items: []Item{{
	Type: "new_release", Service: "web", To: "1.28.0", At: time.Now(),
	Text: "web: new release 1.28.0 <minor> & more", URL: "https://github.com/nginx/nginx/releases/tag/release-1.28.0",
}}}

func digest() Message {
	m := newRelease
	m.Digest = true
	m.Items = append(m.Items, Item{Type: "drift_detected", Text: "web in prod: runs 1.27.2, behind 1.28.0 upstream", At: time.Now()})
	return m
}

func TestDiscord(t *testing.T) {
	srv, got := capture(t, http.StatusNoContent)
	if err := (Discord{HTTP: srv.Client()}).Send(context.Background(), channel("discord", `{"url":"`+srv.URL+`/api/webhooks/1/x"}`), digest()); err != nil {
		t.Fatal(err)
	}
	var p struct {
		Content  string `json:"content"`
		Mentions struct {
			Parse []string `json:"parse"`
		} `json:"allowed_mentions"`
	}
	if err := json.Unmarshal([]byte(got.body), &p); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(p.Content, "**Goliash: 2 updates in Default**\n• web: new release") ||
		!strings.Contains(p.Content, "[release notes](<https://github.com/nginx/nginx/releases/tag/release-1.28.0>)") || p.Mentions.Parse == nil {
		t.Fatalf("payload %s", got.body)
	}
}

func TestTelegram(t *testing.T) {
	srv, got := capture(t, http.StatusOK)
	tg := Telegram{HTTP: srv.Client(), API: srv.URL}
	if err := tg.Send(context.Background(), channel("telegram", `{"bot_token":"123:ABC","chat_id":"-100"}`), newRelease); err != nil {
		t.Fatal(err)
	}
	if got.path != "/bot123:ABC/sendMessage" {
		t.Fatalf("path %s", got.path)
	}
	if !strings.Contains(got.body, `"parse_mode":"HTML"`) || !strings.Contains(got.body, `"chat_id":"-100"`) {
		t.Fatalf("payload %s", got.body)
	}
	var full struct {
		Text string `json:"text"`
	}
	_ = json.Unmarshal([]byte(got.body), &full)
	if !strings.Contains(full.Text, "new release 1.28.0 &lt;minor&gt; &amp; more") ||
		!strings.Contains(full.Text, `<a href="https://github.com/nginx/nginx/releases/tag/release-1.28.0">release notes</a>`) {
		t.Fatalf("text not escaped or link lost: %s", full.Text)
	}

	bad, _ := capture(t, http.StatusBadRequest)
	err := (Telegram{HTTP: bad.Client(), API: bad.URL}).Send(context.Background(), channel("telegram", `{"bot_token":"123:SECRET","chat_id":"x"}`), newRelease)
	if err == nil || strings.Contains(err.Error(), "SECRET") || !strings.Contains(err.Error(), "chat not found") {
		t.Fatalf("error must explain without the token: %v", err)
	}
	if err := tg.Send(context.Background(), channel("telegram", `{"chat_id":"x"}`), newRelease); err == nil {
		t.Fatal("missing bot token accepted")
	}
}

func TestNtfy(t *testing.T) {
	srv, got := capture(t, http.StatusOK)
	if err := (Ntfy{HTTP: srv.Client()}).Send(context.Background(), channel("ntfy", `{"url":"`+srv.URL+`/goliash","token":"tk_1"}`), newRelease); err != nil {
		t.Fatal(err)
	}
	if got.path != "/goliash" || got.header.Get("Authorization") != "Bearer tk_1" || got.header.Get("Title") != "Goliash" ||
		got.header.Get("Click") != newRelease.Items[0].URL || !strings.HasPrefix(got.header.Get("Content-Type"), "text/plain") {
		t.Fatalf("headers %v", got.header)
	}
	if got.body != "web: new release 1.28.0 <minor> & more" {
		t.Fatalf("body %q", got.body)
	}
	if err := (Ntfy{HTTP: srv.Client()}).Send(context.Background(), channel("ntfy", `{"url":"`+srv.URL+`/goliash"}`), digest()); err != nil {
		t.Fatal(err)
	}
	if got.header.Get("Title") != "Goliash: 2 updates in Default" || got.header.Get("Click") != "" || strings.Count(got.body, "\n") != 1 {
		t.Fatalf("digest: %v %q", got.header, got.body)
	}
}

func TestTruncate(t *testing.T) {
	if got := truncate(strings.Repeat("é", 10), 5); got != "éééé…" {
		t.Fatalf("truncate = %q", got)
	}
}

func TestGrafanaAnnotations(t *testing.T) {
	srv, got := capture(t, http.StatusOK)
	at := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	msg := Message{Items: []Item{{Type: "version_changed", Service: "web", Environment: "prod", Text: "web in prod: 1.27.2 → 1.28.0", At: at}}}
	if err := (Grafana{HTTP: srv.Client()}).Send(context.Background(), channel("grafana", `{"url":"`+srv.URL+`/","token":"glsa_x"}`), msg); err != nil {
		t.Fatal(err)
	}
	var a struct {
		Time int64    `json:"time"`
		Tags []string `json:"tags"`
		Text string   `json:"text"`
	}
	_ = json.Unmarshal([]byte(got.body), &a)
	if got.path != "/api/annotations" || got.header.Get("Authorization") != "Bearer glsa_x" || a.Time != at.UnixMilli() ||
		strings.Join(a.Tags, ",") != "goliash,version_changed,web,prod" || a.Text != msg.Items[0].Text {
		t.Fatalf("annotation %s %v %+v", got.path, got.header, a)
	}
	if err := (Grafana{HTTP: srv.Client()}).Send(context.Background(), channel("grafana", `{"url":"`+srv.URL+`"}`), msg); err == nil {
		t.Fatal("missing token accepted")
	}
}
