// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package ui

import (
	"context"
	"encoding/json"
	"net/url"
	"strings"
	"testing"

	"github.com/pipozzz/goliash/internal/notifier"
	"github.com/pipozzz/goliash/internal/store"
)

func TestEditRule(t *testing.T) {
	e := newUIEnv(t)
	ctx := context.Background()
	sc := e.ws.Scope()
	for _, name := range []string{"ops", "pay"} {
		_, _ = e.st.CreateChannel(ctx, store.Channel{Scope: sc, Type: "slack", Name: name, Config: json.RawMessage(`{"url":"https://hooks.slack.com/x"}`)})
	}
	member := e.as(store.RoleMember)
	add := url.Values{"channel": {"ops"}, "mode": {"instant"}, "events": {"new_release"}, "services": {"web"}}
	if _, body, _ := post(t, member, e.srv.URL+"/notifications/rules", add); !strings.Contains(body, "Rule added") {
		t.Fatalf("add: %s", body)
	}
	rules, _ := e.st.ListRules(ctx, sc)
	id := rules[0].ID
	_, page := get(t, member, e.srv.URL+"/notifications/rules/"+id, nil)
	if !strings.Contains(page, "Edit rule") || !strings.Contains(page, `value="web"`) || !strings.Contains(page, `value="new_release" checked`) {
		t.Fatalf("edit page not filled in: %s", page)
	}
	edit := url.Values{"channel": {"pay"}, "mode": {"weekly"}, "digest_hour": {"6"}, "events": {"updates_plan", "drift_detected"}, "owners": {"team-pay"}, "min_jump": {"major"}}
	if _, body, _ := post(t, member, e.srv.URL+"/notifications/rules/"+id, edit); !strings.Contains(body, "Rule saved") {
		t.Fatalf("save: %s", body)
	}
	rules, _ = e.st.ListRules(ctx, sc)
	var f notifier.Filter
	_ = json.Unmarshal(rules[0].Filter, &f)
	chans, _ := e.st.ListChannels(ctx, sc)
	if rules[0].Mode != "weekly" || *f.DigestHour != 6 || f.Owners[0] != "team-pay" || len(f.Services) != 0 || string(f.MinJump) != "major" ||
		strings.Join(rules[0].EventTypes, ",") != "updates_plan,drift_detected" || rules[0].ChannelID != chans[1].ID {
		t.Fatalf("saved %+v %+v", rules[0], f)
	}
	if _, body, _ := post(t, member, e.srv.URL+"/notifications/rules/"+id, url.Values{"channel": {"pay"}}); !strings.Contains(body, "at least one kind") {
		t.Fatal("a rule without events was saved")
	}
	if code, _ := get(t, e.as(store.RoleViewer), e.srv.URL+"/notifications/rules/"+id, nil); code != 403 {
		t.Fatalf("viewer may edit: %d", code)
	}
}
