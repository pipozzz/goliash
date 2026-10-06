// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package notifier

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

func TestTeamsCard(t *testing.T) {
	s := string(marshal(teamsCard(richMessage())))
	for _, want := range []string{
		`"contentType":"application/vnd.microsoft.card.adaptive"`, `"type":"AdaptiveCard"`, `"version":"1.4"`,
		`✨ **payments-api**`, `"title":"Version","value":"1.5.0 → 1.6.0"`, `"title":"Release notes","type":"Action.OpenUrl","url":"https://example.com/notes"`,
		`"style":"attention"`, `"title":"Open in Goliash","type":"Action.OpenUrl","url":"https://goliash.example.com/"`, `1 new release · 1 end of life · 1 agent silent`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("card misses %q:\n%s", want, s)
		}
	}
}

func TestGoogleChatCard(t *testing.T) {
	s := string(marshal(googleChatCard(richMessage())))
	for _, want := range []string{
		`"cardsV2"`, `"topLabel":"✨ New release"`, `<b>payments-api</b>`, `1.5.0 → 1.6.0`, `&lt;b&gt;&amp;&lt;/b&gt;`,
		`"url":"https://example.com/notes"`, `"text":"Open in Goliash"`, `"imageUrl":"https://goliash.example.com/static/icon-192.png"`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("card misses %q:\n%s", want, s)
		}
	}
	if strings.Contains(s, "<b>&</b>") {
		t.Error("release name markup not escaped")
	}
}

func TestTeamsAndGoogleChatPost(t *testing.T) {
	for _, typ := range []string{"teams", "gchat"} {
		srv, got := capture(t, http.StatusAccepted)
		sender := DefaultSenders(srv.Client(), SMTPConfig{})[typ]
		if err := sender.Send(context.Background(), channel(typ, `{"url":"`+srv.URL+`/hook"}`), digest()); err != nil {
			t.Fatalf("%s: %v", typ, err)
		}
		if !strings.Contains(got.body, "Goliash: 2 updates in Default") {
			t.Errorf("%s body %s", typ, got.body)
		}
	}
}
