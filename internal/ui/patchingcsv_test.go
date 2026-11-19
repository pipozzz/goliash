// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package ui

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/pipozzz/goliash/internal/store"
)

// The time-to-patch CSV has a row per upgrade deployed to production: the versions, since when the service was
// behind, the deploy and the days between.
func TestPatchingCSV(t *testing.T) {
	e := newUIEnv(t)
	ctx := context.Background()
	sc := e.ws.Scope()
	envs, _ := e.st.ListEnvironments(ctx, sc)
	prod := envs[len(envs)-1].ID
	api, _ := e.st.EnsureService(ctx, sc, "api")
	api.Owner = "team-pay"
	if err := e.st.UpdateService(ctx, api); err != nil {
		t.Fatal(err)
	}
	deployed := time.Now().UTC().Add(-48 * time.Hour)
	published := deployed.Add(-10 * 24 * time.Hour)
	if _, err := e.st.InsertReleases(ctx, sc, api.ID, []string{"1.1.0"}); err != nil {
		t.Fatal(err)
	}
	if err := e.st.SetReleaseInfo(ctx, sc, api.ID, "1.1.0", published, ""); err != nil {
		t.Fatal(err)
	}
	if err := e.st.InsertEvent(ctx, sc, store.Event{ID: store.NewID(), Type: "version_changed", ServiceID: api.ID, EnvironmentID: prod, FromVersion: "1.0.0", ToVersion: "1.1.0", At: deployed}); err != nil {
		t.Fatal(err)
	}
	viewer := e.as(store.RoleViewer)
	code, body := get(t, viewer, e.srv.URL+"/security/patching.csv", nil)
	want := "service,owner,from,to,behind_since,deployed,days\napi,team-pay,1.0.0,1.1.0," + published.Format(time.DateOnly) + "," + deployed.Format(time.DateOnly) + ",10\n"
	if code != 200 || body != want {
		t.Errorf("csv %d:\n%s\nwant:\n%s", code, body, want)
	}
	if _, page := get(t, viewer, e.srv.URL+"/security", nil); !strings.Contains(page, `href="/security/patching.csv"`) {
		t.Error("the Security page does not link the upgrades behind its time-to-patch chart")
	}
}
