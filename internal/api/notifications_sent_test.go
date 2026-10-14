// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/pipozzz/goliash/internal/store"
)

func TestNotificationsSent(t *testing.T) {
	e := newPublicEnv(t)
	ctx := context.Background()
	slack, err := e.st.CreateChannel(ctx, store.Channel{Scope: e.sc, Type: "slack", Name: "ops", Config: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	bell, err := e.st.CreateChannel(ctx, store.Channel{Scope: e.sc, Type: "inapp", Name: "bell", Config: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	toSlack, _ := e.st.CreateRule(ctx, store.Rule{Scope: e.sc, ChannelID: slack.ID, Mode: "instant"})
	toBell, _ := e.st.CreateRule(ctx, store.Rule{Scope: e.sc, ChannelID: bell.ID, Mode: "instant"})
	alert := json.RawMessage(`{"type":"drift_detected","service":"web","environment":"prod","apps":["shop"],"text":"web drifts in prod","at":"2026-10-01T00:00:00Z"}`)
	for _, r := range []string{toSlack.ID, toBell.ID} {
		if err := e.st.RecordSent(ctx, e.sc, r, []json.RawMessage{alert}); err != nil {
			t.Fatal(err)
		}
	}

	code, body := e.call(http.DefaultClient, http.MethodGet, "/api/v1/notifications/sent", e.token, "")
	var got []struct {
		Title string `json:"title"`
		Mode  string `json:"mode"`
		Items []map[string]any
	}
	if code != http.StatusOK || json.Unmarshal([]byte(body), &got) != nil {
		t.Fatalf("%d %s", code, body)
	}
	// Sent to two channels, listed once; the rule-only fields stay out.
	if len(got) != 1 || len(got[0].Items) != 1 || got[0].Mode != "instant" || !strings.Contains(got[0].Title, "web") ||
		got[0].Items[0]["service"] != "web" || got[0].Items[0]["apps"] != nil {
		t.Fatalf("sent: %s", body)
	}
	later := url.QueryEscape(time.Now().Add(time.Hour).UTC().Format(time.RFC3339))
	if _, body = e.call(http.DefaultClient, http.MethodGet, "/api/v1/notifications/sent?since="+later, e.token, ""); body != "[]\n" {
		t.Errorf("since later: %q", body)
	}
}
