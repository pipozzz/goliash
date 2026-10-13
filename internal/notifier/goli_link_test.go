// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package notifier

import (
	"encoding/json"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestAskGoliFromAnAlert(t *testing.T) {
	vuln := Item{Type: "vulnerability", Service: "payments-api", Environment: "prod", Text: "payments-api @ prod: CVE-2021-44228 in log4j-core 2.14.1, fixed in 2.15.0", At: time.Now()}
	msg := Message{Workspace: "Prod", Items: []Item{vuln}, Link: "https://goliash.example.com", Goli: true}

	link := goliLink(msg)
	u, err := url.Parse(link)
	if err != nil || u.Path != "/" || !strings.Contains(u.Query().Get("goli"), "CVE-2021-44228") {
		t.Fatalf("link %q", link)
	}
	if b, _ := json.Marshal(slackBlocks(msg)); !strings.Contains(string(b), `"Ask Goli"`) {
		t.Error("no Ask Goli in Slack")
	}
	if b, _ := json.Marshal(teamsCard(msg)); !strings.Contains(string(b), `"Ask Goli"`) {
		t.Error("no Ask Goli in Teams")
	}
	if page, _ := emailHTML(msg); !strings.Contains(page, ">Ask Goli<") {
		t.Error("no Ask Goli in e-mail")
	}

	update := Item{Type: "update", Service: "postgres", Environment: "prod", From: "15.6", To: "17.2", Text: "x"}
	if q := goliQuestion(update); !strings.HasPrefix(q, "Brief me on upgrading postgres in prod from 15.6 to 17.2") {
		t.Errorf("update question %q", q)
	}

	for name, m := range map[string]Message{
		"Goli off":       {Items: []Item{vuln}, Link: msg.Link},
		"no public URL":  {Items: []Item{vuln}, Goli: true},
		"a digest":       {Items: []Item{vuln, vuln}, Link: msg.Link, Goli: true},
		"drift resolved": {Items: []Item{{Type: "drift_resolved", Service: "x", Text: "x"}}, Link: msg.Link, Goli: true},
	} {
		if goliLink(m) != "" {
			t.Errorf("%s: offered Goli", name)
		}
	}
}
