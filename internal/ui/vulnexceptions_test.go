// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package ui

import (
	"context"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/pipozzz/goliash/internal/store"
)

// An admin marks a vulnerability as not affecting a service: it leaves the service's findings and the vulnerabilities
// page, shows with its reason on the service page and in the monthly report, and counts again once withdrawn.
func TestNotAffectedFlow(t *testing.T) {
	e := newUIEnv(t)
	ctx := context.Background()
	sc := e.ws.Scope()
	envs, _ := e.st.ListEnvironments(ctx, sc)
	prod := envs[len(envs)-1]
	tg, _ := e.st.CreateTarget(ctx, store.Target{Scope: sc, EnvironmentID: prod.ID, Platform: "kubernetes", Name: "prod-vex"})
	svc, _ := e.st.EnsureService(ctx, sc, "edge")
	if err := e.st.ApplySnapshot(ctx, store.SnapshotChanges{Scope: sc, TargetID: tg.ID, SnapshotID: store.NewID(), At: time.Now(), Upsert: []store.Instance{{
		TargetID: tg.ID, EnvironmentID: prod.ID, ServiceID: svc.ID, WorkloadID: "edge", WorkloadKind: "deployment", WorkloadName: "edge",
		ContainerName: "nginx", Image: "ghcr.io/acme/edge:1.27.1", Tag: "1.27.1", Digest: "sha256:cc", Running: 1, IsMain: true,
	}}}); err != nil {
		t.Fatal(err)
	}
	pkg := "pkg:deb/debian/nginx@1.27.1-1~bookworm"
	_ = e.st.SetImageSBOM(ctx, sc, store.ImageSBOM{Repo: "ghcr.io/acme/edge", Digest: "sha256:cc", Purls: []string{pkg}, Source: "sbom"})
	_ = e.st.SetPackageVulns(ctx, map[string][]string{pkg: {"DEBIAN-CVE-2023-44487"}})
	_ = e.st.SetVuln(ctx, store.Vuln{ID: "DEBIAN-CVE-2023-44487", Aliases: []string{"CVE-2023-44487"}, Severity: "HIGH", Fixes: map[string][]string{}})
	_ = e.st.SetKnownExploited(ctx, []store.Exploited{{CVE: "CVE-2023-44487", DateAdded: time.Now().Format(time.DateOnly)}})

	admin, member := e.as(store.RoleAdmin), e.as(store.RoleMember)
	_, page := get(t, admin, e.srv.URL+"/services/edge", nil)
	if !strings.Contains(page, `id="vuln-CVE-2023-44487"`) || !strings.Contains(page, "Not affected?") {
		t.Fatal("no Not affected form for an admin")
	}
	if _, page := get(t, member, e.srv.URL+"/services/edge", nil); strings.Contains(page, "Not affected?") {
		t.Error("a member can mark vulnerabilities not affected")
	}
	if code, _, _ := post(t, member, e.srv.URL+"/security/exceptions", url.Values{"cve": {"CVE-2023-44487"}, "service": {"edge"}, "justification": {"fixed"}, "note": {"x"}}); code != 403 {
		t.Errorf("a member marked it: %d", code)
	}
	_, body, _ := post(t, admin, e.srv.URL+"/security/exceptions", url.Values{"cve": {"CVE-2023-44487"}, "service": {"edge"}, "justification": {"nonsense"}, "note": {"x"}})
	if !strings.Contains(body, "Pick why") {
		t.Errorf("a bad reason was taken: %s", body[:min(len(body), 300)])
	}
	_, body, _ = post(t, admin, e.srv.URL+"/security/exceptions", url.Values{
		"cve": {"CVE-2023-44487"}, "service": {"edge"}, "justification": {"fixed"}, "note": {"fixed upstream in nginx 1.25.3"}, "review": {"90"},
	})
	if !strings.Contains(body, "does not affect edge") {
		t.Fatalf("mark: %s", body[:min(len(body), 300)])
	}
	_, page = get(t, member, e.srv.URL+"/services/edge", nil)
	if strings.Contains(page, `id="vuln-CVE-2023-44487"`) || !strings.Contains(page, "<h3>Not affected</h3>") || !strings.Contains(page, "fixed upstream in nginx 1.25.3") {
		t.Error("the service page still lists it, or not as not affected")
	}
	if _, page := get(t, member, e.srv.URL+"/security/vulns?kev=1", nil); strings.Contains(page, "CVE-2023-44487") {
		t.Error("the vulnerabilities page still lists it")
	}
	if _, rep := get(t, member, e.srv.URL+"/security/report?month="+time.Now().UTC().Format("2006-01"), nil); !strings.Contains(rep, "<h2>Not affected</h2>") || !strings.Contains(rep, "fixed upstream in nginx 1.25.3") {
		t.Error("the report lacks the exception")
	}
	_, page = get(t, admin, e.srv.URL+"/services/edge", nil)
	m := regexp.MustCompile(`/security/exceptions/([^/"]+)/close`).FindStringSubmatch(page)
	if m == nil {
		t.Fatal("no Withdraw")
	}
	_, body, _ = post(t, admin, e.srv.URL+"/security/exceptions/"+m[1]+"/close", url.Values{"return": {"/services/edge#security"}})
	if !strings.Contains(body, "counts again") {
		t.Fatalf("withdraw: %s", body[:min(len(body), 300)])
	}
	if _, page := get(t, member, e.srv.URL+"/services/edge", nil); !strings.Contains(page, `id="vuln-CVE-2023-44487"`) {
		t.Error("withdrawn, but not listed again")
	}
}
