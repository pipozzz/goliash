// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package versions

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/pipozzz/goliash/internal/store"
)

func TestBetween(t *testing.T) {
	var rels []store.Release
	for _, v := range strings.Fields("1.4.2 1.5.0 1.5.1 1.6.0 1.6.0-alpine 1.7.0-rc.1 1.7.0 1.4.2-alpine") {
		rels = append(rels, store.Release{Version: v})
	}
	names := func(rs []store.Release) string {
		var out []string
		for _, r := range rs {
			out = append(out, r.Version)
		}
		return strings.Join(out, " ")
	}
	if got := names(between(rels, "1.4.2", "1.6.0")); got != "1.6.0 1.5.1 1.5.0" {
		t.Fatalf("between 1.4.2 and 1.6.0: %s", got)
	}
	if got := names(between(rels, "1.4.2-alpine", "1.6.0-alpine")); got != "1.6.0-alpine" {
		t.Fatalf("variants compare like with like: %s", got)
	}
	if got := names(between(rels, "1.6.0", "1.7.0")); got != "1.7.0" {
		t.Fatalf("prereleases are left out unless waiting: %s", got)
	}
	if got := between(rels, "latest", "1.7.0"); got != nil {
		t.Fatalf("unparseable running version: %v", got)
	}
}

// A service compared per application waits for promotion per application.
func TestPromotionsPerApp(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, "sqlite://:memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ws, _ := st.EnsureDefaultWorkspace(ctx)
	sc := ws.Scope()
	_, _ = st.CreateEnvironment(ctx, sc, "staging", 20)
	prod, _ := st.CreateEnvironment(ctx, sc, "prod", 30)
	pg, _ := st.EnsureService(ctx, sc, "postgres")
	for _, app := range []string{"webshop", "identity"} {
		if _, err := st.OpenDrift(ctx, store.Drift{Scope: sc, ServiceID: pg.ID, App: app, EnvironmentID: prod.ID, Kind: "env",
			Detail: json.RawMessage(`{"running":"15.6","other":"15.7","other_in":"staging"}`)}); err != nil {
			t.Fatal(err)
		}
	}
	o, err := LoadOverview(ctx, st, sc)
	if err != nil {
		t.Fatal(err)
	}
	ps, err := Promotions(ctx, st, sc, o)
	if err != nil || len(ps) != 2 || ps[0].From.Name != "staging" {
		t.Fatalf("promotions %+v %v", ps, err)
	}
	if apps := ps[0].App + "," + ps[1].App; apps != "webshop,identity" && apps != "identity,webshop" {
		t.Fatalf("applications %s", apps)
	}
}
