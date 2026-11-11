// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package ui

import (
	"context"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/pipozzz/goliash/internal/store"
)

// An exploited vulnerability with no fix asks for a mitigation; a member records one, and it shows on the
// vulnerabilities page, on Security and in the monthly report.
func TestMitigationFlow(t *testing.T) {
	e := newUIEnv(t)
	ctx := context.Background()
	sc := e.ws.Scope()
	envs, _ := e.st.ListEnvironments(ctx, sc)
	prod := envs[len(envs)-1]
	tg, _ := e.st.CreateTarget(ctx, store.Target{Scope: sc, EnvironmentID: prod.ID, Platform: "kubernetes", Name: "prod-mit"})
	svc, _ := e.st.EnsureService(ctx, sc, "shop-api")
	if err := e.st.ApplySnapshot(ctx, store.SnapshotChanges{Scope: sc, TargetID: tg.ID, SnapshotID: store.NewID(), At: time.Now(), Upsert: []store.Instance{{
		TargetID: tg.ID, EnvironmentID: prod.ID, ServiceID: svc.ID, WorkloadID: "shop", WorkloadKind: "deployment", WorkloadName: "shop-api",
		ContainerName: "app", Image: "ghcr.io/acme/shop-api:2.0.0", Tag: "2.0.0", Digest: "sha256:bb", Running: 1, IsMain: true,
	}}}); err != nil {
		t.Fatal(err)
	}
	pkg := "pkg:npm/left-pad@1.0.0"
	_ = e.st.SetImageSBOM(ctx, sc, store.ImageSBOM{Repo: "ghcr.io/acme/shop-api", Digest: "sha256:bb", Purls: []string{pkg}, Source: "sbom"})
	_ = e.st.SetPackageVulns(ctx, map[string][]string{pkg: {"GHSA-zzzz"}})
	_ = e.st.SetVuln(ctx, store.Vuln{ID: "GHSA-zzzz", Aliases: []string{"CVE-2026-1234"}, Severity: "HIGH", Fixes: map[string][]string{}})
	_ = e.st.SetKnownExploited(ctx, []store.Exploited{{CVE: "CVE-2026-1234", DateAdded: time.Now().Format(time.DateOnly)}})

	member, viewer := e.as(store.RoleMember), e.as(store.RoleViewer)
	_, page := get(t, member, e.srv.URL+"/security/vulns?kev=1", nil)
	if !strings.Contains(page, ">no fix<") || !strings.Contains(page, "shop-api (prod)") || !strings.Contains(page, "Record a mitigation") {
		t.Fatal("no call for a mitigation")
	}
	if _, page := get(t, viewer, e.srv.URL+"/security/vulns?kev=1", nil); strings.Contains(page, "Record a mitigation") {
		t.Error("a viewer can record mitigations")
	}
	// The service's page has it too, with the form, where the alert leads.
	if _, page := get(t, member, e.srv.URL+"/services/shop-api", nil); !strings.Contains(page, `id="vuln-CVE-2026-1234"`) ||
		!strings.Contains(page, "What to do:") || !strings.Contains(page, `name="return" value="/services/shop-api#vuln-CVE-2026-1234"`) {
		t.Fatal("the service page has no mitigation form")
	}
	_, body, _ := post(t, member, e.srv.URL+"/security/mitigations", url.Values{
		"cve": {"CVE-2026-1234"}, "place": {svc.ID + "|" + prod.ID}, "kind": {"waf"}, "measure": {"WAF rule drops the payload"},
		"review": {"30"}, "return": {"/services/shop-api#vuln-CVE-2026-1234"},
	})
	if !strings.Contains(body, "Mitigation recorded") {
		t.Fatalf("record: %s", body[:min(len(body), 300)])
	}
	if _, page := get(t, viewer, e.srv.URL+"/services/shop-api", nil); !strings.Contains(page, "WAF rule drops the payload") || strings.Contains(page, "Record a mitigation") {
		t.Error("the service page does not show the mitigation")
	}
	_, page = get(t, viewer, e.srv.URL+"/security/vulns?kev=1", nil)
	if !strings.Contains(page, ">mitigated<") || !strings.Contains(page, "WAF rule drops the payload") || strings.Contains(page, "Record a mitigation") {
		t.Error("the mitigation is not shown")
	}
	if _, sec := get(t, viewer, e.srv.URL+"/security", nil); !strings.Contains(sec, "Exploited, no fix yet") || !strings.Contains(sec, "WAF rule drops the payload") {
		t.Error("Security lacks the panel")
	}
	if _, rep := get(t, viewer, e.srv.URL+"/security/report?month="+time.Now().UTC().Format("2006-01"), nil); !strings.Contains(rep, "<h2>Mitigations</h2>") || !strings.Contains(rep, "in force") {
		t.Error("the report lacks mitigations")
	}
	if code, _, _ := post(t, member, e.srv.URL+"/security/mitigations", url.Values{"cve": {"CVE-2026-1234"}, "place": {svc.ID + "|" + prod.ID}, "kind": {"magic"}, "measure": {"x"}, "review": {"30"}}); code >= 500 {
		t.Errorf("a bad kind: %d", code)
	}
}
