// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package ui

import (
	"context"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/pipozzz/goliash/internal/notifier"
	"github.com/pipozzz/goliash/internal/store"
)

// A team's card says how it patches and, until a rule tells the team, offers to: two rules on the picked channel.
func TestNotifyTeam(t *testing.T) {
	e := newUIEnv(t)
	ctx := context.Background()
	m, viewer := e.as(store.RoleMember), e.as(store.RoleViewer)
	svc, _ := e.st.GetServiceByName(ctx, e.ws.Scope(), e.serviceOf("evil"))
	svc.Owner = "team-web"
	if err := e.st.UpdateService(ctx, svc); err != nil {
		t.Fatal(err)
	}
	ch, err := e.st.CreateChannel(ctx, store.Channel{Scope: e.ws.Scope(), Type: "slack", Name: "web-team", Config: []byte(`{"url":"https://hooks.slack.com/services/x"}`)})
	if err != nil {
		t.Fatal(err)
	}
	_, page := get(t, m, e.srv.URL+"/teams", nil)
	if !strings.Contains(page, "Notify the team") || !strings.Contains(page, "no upgrade in 90 days") {
		t.Fatal("the team card neither says how it patches nor offers to notify it")
	}
	if _, page = get(t, viewer, e.srv.URL+"/teams", nil); strings.Contains(page, `action="/teams/notify"`) {
		t.Error("a viewer is offered to notify the team")
	}
	if code, _, _ := post(t, viewer, e.srv.URL+"/teams/notify", url.Values{"team": {"team-web"}, "channel": {ch.ID}}); code != 403 && code != 404 {
		t.Fatalf("a viewer notified a team: %d", code)
	}
	post(t, m, e.srv.URL+"/teams/notify", url.Values{"team": {"team-web"}, "channel": {ch.ID}})
	rules, _ := e.st.ListRules(ctx, e.ws.Scope())
	var instant, weekly bool
	for _, r := range rules {
		if r.ChannelID != ch.ID || !strings.Contains(string(r.Filter), `"owners":["team-web"]`) {
			continue
		}
		instant = instant || (r.Mode == "instant" && slices.Contains(r.EventTypes, notifier.EventDeadline) && slices.Contains(r.EventTypes, "vulnerability") && strings.Contains(string(r.Filter), `"production":true`))
		weekly = weekly || (r.Mode == "weekly" && slices.Contains(r.EventTypes, notifier.EventPostureReport))
	}
	if !instant || !weekly {
		t.Fatalf("rules %+v", rules)
	}
	if _, page = get(t, m, e.srv.URL+"/teams", nil); strings.Contains(page, "Notify the team") || !strings.Contains(page, "its rules") {
		t.Error("a notified team is offered again")
	}
}
