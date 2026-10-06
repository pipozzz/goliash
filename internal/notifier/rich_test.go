// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package notifier

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func richMessage() Message {
	at := time.Date(2026, 10, 6, 9, 0, 0, 0, time.UTC)
	items := []Item{
		{Type: "new_release", Service: "payments-api", From: "1.5.0", To: "1.6.0", Note: "minor", URL: "https://example.com/notes", At: at},
		{Type: "drift_detected", Service: "postgres", App: "cefiro", Environment: "prod", From: "13.4", To: "13", Note: "eol", At: at},
		{Type: "agent_stale", Target: "eu-cluster", At: at},
	}
	for i := range items {
		items[i].Text = Describe(items[i])
	}
	items[0].Text += " <b>&</b>" // markup in a release name stays text
	return Message{Workspace: "Default", Digest: true, Items: items, Link: "https://goliash.example.com"}
}

// marshal encodes like Slack reads it, without Go's \u0026 escapes, to compare.
func marshal(v any) []byte {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
	return b.Bytes()
}

func TestSlackBlocks(t *testing.T) {
	b := marshal(slackBlocks(richMessage()))
	s := string(b)
	for _, want := range []string{
		`"type":"header"`, `✨ *payments-api*`, "`1.5.0` → `1.6.0`", `"Release notes"`, `https://example.com/notes`,
		`⛔ *postgres · cefiro · prod*`, `🔌 *Agent silent*`, `Open in Goliash`, `https://goliash.example.com/?q=postgres`,
		`&lt;b&gt;&amp;&lt;/b&gt;`, `1 new release · 1 end of life · 1 agent silent`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("blocks miss %q:\n%s", want, s)
		}
	}
	// One item: no header, the button opens the item.
	one := richMessage()
	one.Digest, one.Items = false, one.Items[1:2]
	b = marshal(slackBlocks(one))
	if strings.Contains(string(b), `"header"`) || !strings.Contains(string(b), `"type":"button","url":"https://goliash.example.com/?q=postgres"`) {
		t.Errorf("single item blocks %s", b)
	}
	// Without a public URL there are no buttons back.
	one.Link = ""
	if b = marshal(slackBlocks(one)); strings.Contains(string(b), "Open in Goliash") {
		t.Errorf("button without a URL: %s", b)
	}
}

func TestDiscordEmbeds(t *testing.T) {
	e := discordEmbeds(richMessage())
	if len(e) != 3 || e[1]["color"] != 0xe5484d || e[0]["url"] != "https://goliash.example.com/?q=payments-api" {
		t.Fatalf("embeds %v", e)
	}
}

func TestEmailHTML(t *testing.T) {
	page, err := emailHTML(richMessage())
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"#1b2a6b", "payments-api", "1.6.0", "End of life", "Open in Goliash", "&lt;b&gt;&amp;&lt;/b&gt;", `href="https://goliash.example.com/?q=postgres"`} {
		if !strings.Contains(page, want) {
			t.Errorf("mail misses %q", want)
		}
	}
	if strings.Contains(page, "<b>&</b>") {
		t.Error("release name not escaped")
	}
}
