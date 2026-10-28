// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package ui

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/pipozzz/goliash/internal/goli"
	"github.com/pipozzz/goliash/internal/notifier"
	"github.com/pipozzz/goliash/internal/store"
)

func TestBell(t *testing.T) {
	e := newUIEnv(t)
	ctx := context.Background()
	sc := e.ws.Scope()
	member := e.as(store.RoleMember)
	if code, body, _ := post(t, e.as(store.RoleAdmin), e.srv.URL+"/notifications/channels", url.Values{"name": {"bell"}, "type": {"inapp"}}); code >= 400 || strings.Contains(body, "Enter") {
		t.Fatalf("creating an In Goliash channel: %d", code)
	}
	chans, _ := e.st.ListChannels(ctx, sc)
	chans = ownChannels(chans)
	if len(chans) != 1 || chans[0].Type != "inapp" {
		t.Fatalf("channels %+v", chans)
	}
	rule, _ := e.st.CreateRule(ctx, store.Rule{Scope: sc, ChannelID: chans[0].ID, Mode: "instant"})
	for _, it := range []notifier.Item{
		{Type: "vulnerability", Service: "payments-api", Environment: "prod", Text: "payments-api @ prod: CVE-2021-44228 in log4j-core 2.14.1"},
		{Type: "drift_detected", Service: "web", Environment: "prod", Text: "web @ prod: behind staging"},
	} {
		payload, _ := json.Marshal(it)
		if _, err := e.st.Enqueue(ctx, store.QueueItem{Scope: sc, RuleID: rule.ID, Payload: payload, DedupKey: it.Text, DueAt: time.Now()}); err != nil {
			t.Fatal(err)
		}
		due, _ := e.st.DueItems(ctx, time.Now().Add(time.Minute), 6)
		_ = e.st.MarkSent(ctx, []string{due[0].ID})
		time.Sleep(3 * time.Millisecond)
	}

	if _, page := get(t, member, e.srv.URL+"/", nil); !strings.Contains(page, `data-unread>2<`) {
		t.Error("the bell does not count 2")
	}
	var list struct {
		Unread int
		Items  []struct {
			ID, Title, URL string
			Unread         bool
		}
	}
	_, body := get(t, member, e.srv.URL+"/ui/notifications", nil)
	if err := json.Unmarshal([]byte(body), &list); err != nil {
		t.Fatal(err)
	}
	if list.Unread != 2 || len(list.Items) != 2 || !list.Items[0].Unread || list.Items[0].URL != "/services/web" || !strings.Contains(list.Items[1].Title, "CVE-2021-44228") {
		t.Errorf("list %+v", list)
	}
	if strings.Contains(body, `"ask"`) {
		t.Error("a question for Goli while Goli is off")
	}
	// With Goli on, each alert comes with a question ready to ask.
	e.ui.goli = goli.New(goli.Config{APIKey: "sk-test", Model: goli.DefaultModel, BaseURL: "http://127.0.0.1:1"}, slog.New(slog.DiscardHandler))
	_, body = get(t, member, e.srv.URL+"/ui/notifications", nil)
	if !strings.Contains(body, `"ask":"Explain this drift alert: why web in prod`) || !strings.Contains(body, `"ask":"Explain this vulnerability alert`) {
		t.Errorf("questions for Goli: %s", body)
	}
	// Opening one reads that one only; listing them reads nothing.
	_, body = get(t, member, e.srv.URL+"/ui/notifications", nil)
	_ = json.Unmarshal([]byte(body), &list)
	if list.Unread != 2 {
		t.Fatalf("listing read something: %d", list.Unread)
	}
	if _, body, _ := post(t, member, e.srv.URL+"/ui/notifications/read", url.Values{"id": {list.Items[0].ID}}); !strings.Contains(body, `"unread":1`) {
		t.Errorf("after reading one: %s", body)
	}
	_, body = get(t, member, e.srv.URL+"/ui/notifications", nil)
	_ = json.Unmarshal([]byte(body), &list)
	if list.Unread != 1 || list.Items[0].Unread || !list.Items[1].Unread {
		t.Errorf("one read, one not: %+v", list)
	}
	if code, _, _ := post(t, member, e.srv.URL+"/ui/notifications/read", url.Values{"id": {""}}); code != http.StatusBadRequest {
		t.Errorf("no id: %d", code)
	}
	if code, _, _ := post(t, member, e.srv.URL+"/ui/notifications/seen", nil); code != http.StatusNoContent && code != http.StatusOK {
		t.Errorf("seen: %d", code)
	}
	if _, body := get(t, member, e.srv.URL+"/ui/notifications/unread", nil); !strings.Contains(body, `"unread":0`) {
		t.Errorf("after looking: %s", body)
	}
	// Someone else has not looked yet.
	if _, body := get(t, e.as(store.RoleViewer), e.srv.URL+"/ui/notifications/unread", nil); !strings.Contains(body, `"unread":2`) {
		t.Errorf("another person: %s", body)
	}
}
