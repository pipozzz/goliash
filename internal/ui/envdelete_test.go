// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package ui

import (
	"net/url"
	"strings"
	"testing"

	"github.com/pipozzz/goliash/internal/store"
)

// An environment nothing reports for can go, with its targets; one with a target that reports cannot.
func TestDeleteDeadEnvironment(t *testing.T) {
	e := newUIEnv(t)
	ctx := t.Context()
	sc := e.ws.Scope()
	old, _ := e.st.CreateEnvironment(ctx, sc, "old", 50)
	if _, err := e.st.CreateTarget(ctx, store.Target{Scope: sc, EnvironmentID: old.ID, Platform: "kubernetes", Name: "gone"}); err != nil {
		t.Fatal(err)
	}
	admin := e.as(store.RoleAdmin)
	if _, page := get(t, admin, e.srv.URL+"/agents", nil); !strings.Contains(page, "Delete environment old and its 1 target?") {
		t.Fatal("no delete for an environment nothing reports for")
	}
	if _, body, _ := post(t, admin, e.srv.URL+"/environments/"+old.ID+"/delete", url.Values{"targets": {"dead"}}); !strings.Contains(body, "Environment old deleted") {
		t.Fatalf("delete: %s", body[:min(len(body), 400)])
	}
	if _, body, _ := post(t, admin, e.srv.URL+"/environments/"+e.prod.ID+"/delete", url.Values{"targets": {"dead"}}); !strings.Contains(body, "still reports") {
		t.Fatalf("a live environment went: %s", body[:min(len(body), 400)])
	}
}
