// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package notifier

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/pipozzz/goliash/internal/store"
)

// An exploited vulnerability with no fix in production asks for a mitigation once; a mitigation is brought up
// before its review; when a fix comes out the mitigation closes and the alert says to upgrade.
func TestMitigations(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	sink := newSink(t)
	e.rule(e.channel("slack", "sec", map[string]any{"url": sink.srv.URL}), "instant", []string{EventDeadline}, Filter{})
	_ = e.st.SetRemediation(ctx, e.sc, store.Remediation{Exploited: 7})
	tg, _ := e.st.CreateTarget(ctx, store.Target{Scope: e.sc, EnvironmentID: e.prod.ID, Platform: "kubernetes", Name: "prod-1"})
	if err := e.st.ApplySnapshot(ctx, store.SnapshotChanges{Scope: e.sc, TargetID: tg.ID, SnapshotID: store.NewID(), At: time.Now(), Upsert: []store.Instance{{
		TargetID: tg.ID, EnvironmentID: e.prod.ID, ServiceID: e.svc.ID, WorkloadID: "w", WorkloadKind: "deployment", WorkloadName: "payments-api",
		ContainerName: "app", Image: "ghcr.io/acme/payments-api:1.4.2", Tag: "1.4.2", Digest: "sha256:aa", Running: 1, IsMain: true,
	}}}); err != nil {
		t.Fatal(err)
	}
	lodash := "pkg:npm/lodash@4.17.20"
	_ = e.st.SetImageSBOM(ctx, e.sc, store.ImageSBOM{Repo: "ghcr.io/acme/payments-api", Digest: "sha256:aa", Purls: []string{lodash}, Source: "sbom"})
	_ = e.st.SetPackageVulns(ctx, map[string][]string{lodash: {"GHSA-0000"}})
	_ = e.st.SetVuln(ctx, store.Vuln{ID: "GHSA-0000", Aliases: []string{"CVE-2026-9999"}, Severity: "CRITICAL", Fixes: map[string][]string{}})
	_ = e.st.SetKnownExploited(ctx, []store.Exploited{{CVE: "CVE-2026-9999", DateAdded: "2026-10-20"}})

	e.clock = time.Date(2026, 10, 21, 9, 0, 0, 0, time.UTC)
	for range 2 {
		if err := e.n.CheckDeadlines(ctx); err != nil {
			t.Fatal(err)
		}
		_ = e.n.DeliverDue(ctx)
	}
	if sink.count() != 1 || !strings.Contains(sink.last(), "CVE-2026-9999 is exploited in the wild and has no fix yet; record a mitigation by ") {
		t.Fatalf("unmitigated: %d %s", sink.count(), sink.last())
	}
	if feed, _ := e.st.NotificationFeed(ctx, e.sc, time.Now().Add(-time.Hour), 10); len(feed) == 0 ||
		!strings.Contains(string(feed[0].Items[0]), `"url":"/security/vulns?env=all\u0026q=CVE-2026-9999"`) &&
			!strings.Contains(string(feed[0].Items[0]), `/security/vulns?env=all&q=CVE-2026-9999`) {
		t.Errorf("the alert does not link the vulnerability: %+v", feed)
	}

	// Mitigated, review in two days: brought up once.
	m, err := e.st.CreateMitigation(ctx, store.Mitigation{
		Scope: e.sc, CVE: "CVE-2026-9999", ServiceID: e.svc.ID, EnvironmentID: e.prod.ID,
		Kind: "waf", Measure: "WAF rule blocks the payload", ReviewAt: e.clock.Add(48 * time.Hour), CreatedBy: "sre@acme.io",
	})
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		_ = e.n.CheckDeadlines(ctx)
		_ = e.n.DeliverDue(ctx)
	}
	if sink.count() != 2 || !strings.Contains(sink.last(), "due for review on 23 Oct: WAF rule blocks the payload") {
		t.Fatalf("review: %d %s", sink.count(), sink.last())
	}

	// A fix is out: the mitigation closes, and the alert says to upgrade.
	_ = e.st.SetVuln(ctx, store.Vuln{ID: "GHSA-0000", Aliases: []string{"CVE-2026-9999"}, Severity: "CRITICAL", Fixes: map[string][]string{"npm|lodash": {"4.17.21"}}})
	for range 2 {
		_ = e.n.CheckDeadlines(ctx)
		_ = e.n.DeliverDue(ctx)
	}
	if sink.count() != 3 || !strings.Contains(sink.last(), "a fix for CVE-2026-9999 is out (lodash 4.17.20 4.17.21); its mitigation is closed") {
		t.Fatalf("fixed: %d %s", sink.count(), sink.last())
	}
	if open, _ := e.st.ListOpenMitigations(ctx, e.sc); len(open) != 0 {
		t.Errorf("still open: %+v", open)
	}
	all, _ := e.st.ListMitigationsSince(ctx, e.sc, time.Time{})
	if len(all) != 1 || all[0].ID != m.ID || all[0].ClosedReason != "a fix is out" {
		t.Errorf("closed: %+v", all)
	}
}
