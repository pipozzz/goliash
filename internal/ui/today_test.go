// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package ui

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/pipozzz/goliash/internal/store"
)

// Today lists what cannot wait (a release cycle past its end of life) and only counts what can (a minor behind).
func TestTodayPage(t *testing.T) {
	e := newUIEnv(t)
	ctx := context.Background()
	viewer := e.as(store.RoleViewer)
	if _, body := get(t, viewer, e.srv.URL+"/today", nil); !strings.Contains(body, "Nothing has to be done today") {
		t.Error("an empty workspace has something to do today")
	}
	envs, _ := e.st.ListEnvironments(ctx, e.ws.Scope())
	prod := envs[len(envs)-1].ID
	pg, _ := e.st.EnsureService(ctx, e.ws.Scope(), "postgres")
	redis, _ := e.st.EnsureService(ctx, e.ws.Scope(), "redis")
	for _, d := range []store.Drift{
		{ServiceID: pg.ID, Kind: "eol", Detail: json.RawMessage(`{"running":"12.6","other":"12","eol":"2024-11-14"}`)},
		{ServiceID: pg.ID, Kind: "upstream", Detail: json.RawMessage(`{"running":"12.6","other":"17.2","jump":"major"}`)},
		{ServiceID: redis.ID, Kind: "upstream", Detail: json.RawMessage(`{"running":"7.2.4","other":"7.4.0","jump":"minor"}`)},
	} {
		d.Scope, d.EnvironmentID = e.ws.Scope(), prod
		if _, err := e.st.OpenDrift(ctx, d); err != nil {
			t.Fatal(err)
		}
	}
	_, body := get(t, viewer, e.srv.URL+"/today", nil)
	for _, want := range []string{"Upgrade first", "postgres", "12.6", "1 upgrade that can wait", `aria-current="page"`} {
		if !strings.Contains(body, want) {
			t.Errorf("today page lacks %q", want)
		}
	}
	if strings.Contains(body, "7.2.4") {
		t.Error("a minor upgrade that can wait is listed for today")
	}
}

func TestUrgentToday(t *testing.T) {
	if !urgentToday(UpdateItem{Level: 0}) || !urgentToday(UpdateItem{Level: 2}) || urgentToday(UpdateItem{Level: 5}) {
		t.Error("exploited and end of life are for today, a minor is not")
	}
}
