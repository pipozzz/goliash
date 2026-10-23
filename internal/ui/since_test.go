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

// The Security page opens with the security brief: nothing new, then a release that fixes vulnerabilities in
// what runs, linked to the service.
func TestSinceYesterday(t *testing.T) {
	e := newUIEnv(t)
	ctx := context.Background()
	v := e.as(store.RoleViewer)
	if _, page := get(t, v, e.srv.URL+"/security", nil); !strings.Contains(page, `id="since-yesterday"`) || !strings.Contains(page, "Nothing new to look at") {
		t.Fatal("no Since yesterday panel, or it is not empty")
	}
	svc, _ := e.st.GetServiceByName(ctx, e.ws.Scope(), e.serviceOf("evil"))
	if _, err := e.st.InsertReleases(ctx, e.ws.Scope(), svc.ID, []string{"1.28.0"}); err != nil {
		t.Fatal(err)
	}
	if err := e.st.SetReleaseSecurity(ctx, e.ws.Scope(), svc.ID, "1.28.0", "CVE-2026-1"); err != nil {
		t.Fatal(err)
	}
	_ = e.ui.checker.EvaluateDrift(ctx, e.ws.Scope())
	_, page := get(t, v, e.srv.URL+"/security", nil)
	for _, want := range []string{">security release</span>", "1.28.0 fixes vulnerabilities; 1.27.2 runs", `<a class="u" href="/updates">1 upgrade fixing vulnerabilities</a>`} {
		if !strings.Contains(page, want) {
			t.Errorf("Since yesterday misses %q", want)
		}
	}
	if strings.Contains(page, "Nothing new to look at") {
		t.Error("still says nothing is new")
	}
}

// While no rule sends vulnerabilities outside Goliash, the Security page says so.
func TestSecurityAlertsHint(t *testing.T) {
	e := newUIEnv(t)
	ctx := context.Background()
	v := e.as(store.RoleViewer)
	if _, page := get(t, v, e.srv.URL+"/security", nil); !strings.Contains(page, "only reach the bell in Goliash") {
		t.Fatal("no hint without an outside rule")
	}
	ch, err := e.st.CreateChannel(ctx, store.Channel{Scope: e.ws.Scope(), Type: "slack", Name: "sec", Config: []byte(`{"url":"https://hooks.slack.com/services/x"}`)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.CreateRule(ctx, store.Rule{Scope: e.ws.Scope(), ChannelID: ch.ID, Mode: "instant"}); err != nil {
		t.Fatal(err)
	}
	if _, page := get(t, v, e.srv.URL+"/security", nil); strings.Contains(page, "only reach the bell in Goliash") {
		t.Error("the hint stays with a rule for every event on Slack")
	}
}

// Production images whose packages are unknown are listed with how to add an SBOM to their build.
func TestSBOMHelper(t *testing.T) {
	e := newUIEnv(t)
	ctx := context.Background()
	v := e.as(store.RoleViewer)
	svc, _ := e.st.GetServiceByName(ctx, e.ws.Scope(), e.serviceOf("evil"))
	if err := e.st.ApplySnapshot(ctx, store.SnapshotChanges{Scope: e.ws.Scope(), SnapshotID: store.NewID(), TargetID: e.tgt.ID, At: time.Now(), Upsert: []store.Instance{{
		TargetID: e.tgt.ID, EnvironmentID: e.prod.ID, ServiceID: svc.ID, WorkloadID: "w9", WorkloadName: "shop", ContainerName: "php",
		Image: "ghcr.io/acme/shop:3", Tag: "3", Digest: "sha256:shop", Running: 1, IsMain: true,
	}}}); err != nil {
		t.Fatal(err)
	}
	_, page := get(t, v, e.srv.URL+"/security", nil)
	if !strings.Contains(page, `id="sbom"`) || !strings.Contains(page, "without an SBOM") || !strings.Contains(page, "sbom: true") ||
		!strings.Contains(page, "--sbom=true --provenance=mode=max") || !strings.Contains(page, "cosign attest") {
		t.Error("no Add an SBOM helper for images without one")
	}
}
