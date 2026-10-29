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

// A distribution's advisory with no fix, for nginx.org's nginx past upstream's fix, is left out as not affected
// (Goliash, from NVD); Debian's own older nginx gets the upstream fix as what to do instead of "no fix yet".
func TestUpstreamNotAffected(t *testing.T) {
	e := newUIEnv(t)
	ctx := context.Background()
	sc := e.ws.Scope()
	envs, _ := e.st.ListEnvironments(ctx, sc)
	prod := envs[len(envs)-1]
	tg, _ := e.st.CreateTarget(ctx, store.Target{Scope: sc, EnvironmentID: prod.ID, Platform: "kubernetes", Name: "prod-up"})
	edge, _ := e.st.EnsureService(ctx, sc, "edge")
	legacy, _ := e.st.EnsureService(ctx, sc, "legacy")
	ins := []store.Instance{
		{
			TargetID: tg.ID, EnvironmentID: prod.ID, ServiceID: edge.ID, WorkloadID: "edge", WorkloadKind: "deployment", WorkloadName: "edge",
			ContainerName: "nginx", Image: "ghcr.io/acme/edge:1.27.1", Tag: "1.27.1", Digest: "sha256:e1", Running: 1, IsMain: true,
		},
		{
			TargetID: tg.ID, EnvironmentID: prod.ID, ServiceID: legacy.ID, WorkloadID: "legacy", WorkloadKind: "deployment", WorkloadName: "legacy",
			ContainerName: "web", Image: "ghcr.io/acme/legacy:3.1", Tag: "3.1", Digest: "sha256:l1", Running: 1, IsMain: true,
		},
	}
	if err := e.st.ApplySnapshot(ctx, store.SnapshotChanges{Scope: sc, TargetID: tg.ID, SnapshotID: store.NewID(), At: time.Now(), Upsert: ins}); err != nil {
		t.Fatal(err)
	}
	upstream, debian := "pkg:deb/debian/nginx@1.27.1-1~bookworm", "pkg:deb/debian/nginx@1.22.1-9+deb12u1"
	_ = e.st.SetImageSBOM(ctx, sc, store.ImageSBOM{Repo: "ghcr.io/acme/edge", Digest: "sha256:e1", Purls: []string{upstream}, Source: "sbom"})
	_ = e.st.SetImageSBOM(ctx, sc, store.ImageSBOM{Repo: "ghcr.io/acme/legacy", Digest: "sha256:l1", Purls: []string{debian}, Source: "sbom"})
	_ = e.st.SetPackageVulns(ctx, map[string][]string{upstream: {"DEBIAN-CVE-2023-44487"}, debian: {"DEBIAN-CVE-2023-44487"}})
	_ = e.st.SetVuln(ctx, store.Vuln{ID: "DEBIAN-CVE-2023-44487", Aliases: []string{"CVE-2023-44487"}, Severity: "HIGH", Fixes: map[string][]string{}})
	_ = e.st.SetKnownExploited(ctx, []store.Exploited{{CVE: "CVE-2023-44487", DateAdded: time.Now().Format(time.DateOnly)}})
	// The kernel's headers bring every kernel CVE; none applies in a container.
	kernel := "pkg:deb/debian/linux-libc-dev@6.1.106-3"
	_ = e.st.SetImageSBOM(ctx, sc, store.ImageSBOM{Repo: "ghcr.io/acme/edge", Digest: "sha256:e1", Purls: []string{upstream, kernel}, Source: "sbom"})
	_ = e.st.SetPackageVulns(ctx, map[string][]string{kernel: {"DEBIAN-CVE-2024-1086", "DEBIAN-CVE-2024-26581"}})
	_ = e.st.SetVuln(ctx, store.Vuln{ID: "DEBIAN-CVE-2024-1086", Aliases: []string{"CVE-2024-1086"}, Severity: "HIGH", Fixes: map[string][]string{}})
	_ = e.st.SetVuln(ctx, store.Vuln{ID: "DEBIAN-CVE-2024-26581", Aliases: []string{"CVE-2024-26581"}, Severity: "HIGH", Fixes: map[string][]string{}})
	if err := e.st.SetUpstreamCVE(ctx, "CVE-2023-44487", []store.UpstreamRange{{Product: "f5:nginx", StartIncl: "1.9.5", EndIncl: "1.25.2"}}); err != nil {
		t.Fatal(err)
	}

	viewer := e.as(store.RoleViewer)
	_, page := get(t, viewer, e.srv.URL+"/services/edge", nil)
	if strings.Contains(page, `id="vuln-CVE-2023-44487"`) || !strings.Contains(page, "(NVD)") || !strings.Contains(page, "upstream nginx 1.27.1 is past the affected versions (up to 1.25.2)") {
		t.Error("edge still lists it, or not as not affected upstream")
	}
	if strings.Contains(page, `id="vuln-CVE-2024-1086"`) || !strings.Contains(page, "<strong>2 vulnerabilities</strong>: linux-libc-dev: a kernel package") {
		t.Error("edge lists kernel CVEs, or not grouped as not affected")
	}
	_ = e.st.SetProductReleases(ctx, "f5:nginx", []store.ProductCycle{{Name: "1.31", Latest: "1.31.8"}, {Name: "1.22", Latest: "1.22.1", EOL: true}})
	_, page = get(t, viewer, e.srv.URL+"/services/legacy", nil)
	if !strings.Contains(page, "upstream nginx fixed it after 1.25.2 · latest 1.31.8") || !strings.Contains(page, "The newest is 1.31.8.") {
		t.Error("legacy lacks the newest release")
	}
	if !strings.Contains(page, `id="vuln-CVE-2023-44487"`) || !strings.Contains(page, "upstream nginx fixed it after 1.25.2") || strings.Contains(page, "no fix yet") ||
		!strings.Contains(page, "a newer nginx fixes CVE-2023-44487") {
		t.Error("legacy lacks the upstream fix")
	}
	_, page = get(t, viewer, e.srv.URL+"/security/vulns?env=all", nil)
	if !strings.Contains(page, "legacy (prod)") || strings.Contains(page, "edge (prod)") || !strings.Contains(page, "upstream nginx fixed it after 1.25.2") {
		t.Error("the vulnerabilities page is wrong")
	}
	if _, rep := get(t, viewer, e.srv.URL+"/security/report?month="+time.Now().UTC().Format("2006-01"), nil); !strings.Contains(rep, "(NVD)") {
		t.Error("the report lacks it")
	}

	// The legacy image's publisher says, in VEX, that its nginx is not affected: that settles it.
	if err := e.st.SetImageVEX(ctx, sc, store.ImageVEX{Repo: "ghcr.io/acme/legacy", Digest: "sha256:l1", Statements: []store.VEXStatement{{
		Vulnerability: "CVE-2023-44487", Status: "not_affected", Justification: "vulnerable_code_not_in_execute_path",
		Packages: []string{"pkg:deb/debian/nginx"}, Author: "Acme Security",
	}}}); err != nil {
		t.Fatal(err)
	}
	_, page = get(t, viewer, e.srv.URL+"/services/legacy", nil)
	if strings.Contains(page, `id="vuln-CVE-2023-44487"`) || !strings.Contains(page, "the vulnerable code is never run, says Acme Security (VEX)") {
		t.Error("legacy ignores its VEX")
	}
}

// Debian 12 has no fix, Debian 13 has: the fix is a trixie base, not a mitigation.
func TestNewerDistroRelease(t *testing.T) {
	e := newUIEnv(t)
	ctx := context.Background()
	sc := e.ws.Scope()
	envs, _ := e.st.ListEnvironments(ctx, sc)
	prod := envs[len(envs)-1]
	tg, _ := e.st.CreateTarget(ctx, store.Target{Scope: sc, EnvironmentID: prod.ID, Platform: "kubernetes", Name: "prod-deb"})
	svc, _ := e.st.EnsureService(ctx, sc, "proxy")
	if err := e.st.ApplySnapshot(ctx, store.SnapshotChanges{Scope: sc, TargetID: tg.ID, SnapshotID: store.NewID(), At: time.Now(), Upsert: []store.Instance{{
		TargetID: tg.ID, EnvironmentID: prod.ID, ServiceID: svc.ID, WorkloadID: "proxy", WorkloadKind: "deployment", WorkloadName: "proxy",
		ContainerName: "nginx", Image: "ghcr.io/acme/proxy:2.0", Tag: "2.0", Digest: "sha256:p1", Running: 1, IsMain: true,
	}}}); err != nil {
		t.Fatal(err)
	}
	pkg := "pkg:deb/debian/nginx@1.22.1-9+deb12u1?os_distro=bookworm&os_name=debian&os_version=12"
	_ = e.st.SetImageSBOM(ctx, sc, store.ImageSBOM{Repo: "ghcr.io/acme/proxy", Digest: "sha256:p1", Purls: []string{pkg}, Source: "sbom"})
	_ = e.st.SetPackageVulns(ctx, map[string][]string{pkg: {"DEBIAN-CVE-2023-44487"}})
	_ = e.st.SetVuln(ctx, store.Vuln{
		ID: "DEBIAN-CVE-2023-44487", Aliases: []string{"CVE-2023-44487"}, Severity: "HIGH",
		Fixes: map[string][]string{"Debian:13|nginx": {"1.24.0-2"}},
	})
	_ = e.st.SetKnownExploited(ctx, []store.Exploited{{CVE: "CVE-2023-44487", DateAdded: time.Now().Format(time.DateOnly)}})

	viewer := e.as(store.RoleViewer)
	_, page := get(t, viewer, e.srv.URL+"/services/proxy", nil)
	if !strings.Contains(page, "fixed in Debian 13 (trixie)") || strings.Contains(page, "no fix yet") ||
		!strings.Contains(page, "Debian 12 (bookworm) has no fix; Debian 13 (trixie) fixes it in nginx 1.24.0-2") || strings.Contains(page, "Record a mitigation") {
		t.Error("the service page does not offer the newer release")
	}
	if _, page := get(t, viewer, e.srv.URL+"/security/vulns?env=all", nil); !strings.Contains(page, "→ fixed in Debian 13 (trixie)") {
		t.Error("the vulnerabilities page does not offer it")
	}
	// Built on python:3.15-bookworm: the image to build on instead is named.
	_ = e.st.SetImageEvidence(ctx, sc, store.ImageEvidence{Repo: "ghcr.io/acme/proxy", Digest: "sha256:p1", CheckedAt: time.Now(), BaseImage: "docker.io/library/python:3.15-bookworm"})
	if _, page := get(t, viewer, e.srv.URL+"/services/proxy", nil); !strings.Contains(page, "rebuild on python:3.15-trixie instead of python:3.15-bookworm") {
		t.Error("the base image to move to is not named")
	}
}

// Two versions in one environment: each image says what is in it, and each vulnerability which version has it.
func TestVulnsPerImage(t *testing.T) {
	e := newUIEnv(t)
	ctx := context.Background()
	sc := e.ws.Scope()
	envs, _ := e.st.ListEnvironments(ctx, sc)
	prod := envs[len(envs)-1]
	tg, _ := e.st.CreateTarget(ctx, store.Target{Scope: sc, EnvironmentID: prod.ID, Platform: "ecs", Name: "ecs-prod"})
	svc, _ := e.st.EnsureService(ctx, sc, "le-certs")
	in := func(w, tag, digest string) store.Instance {
		return store.Instance{
			TargetID: tg.ID, EnvironmentID: prod.ID, ServiceID: svc.ID, WorkloadID: w, WorkloadKind: "ecs_service", WorkloadName: w,
			ContainerName: "app", Image: "1.dkr.ecr.us-west-2.amazonaws.com/le-certs:" + tag, Tag: tag, Digest: digest, Running: 1, IsMain: true,
		}
	}
	_ = e.st.ApplySnapshot(ctx, store.SnapshotChanges{Scope: sc, TargetID: tg.ID, SnapshotID: store.NewID(), At: time.Now(), Upsert: []store.Instance{
		in("le-certs", "v2.3", "sha256:new"), in("qa-le-certs", "v2.1.1", "sha256:old"),
	}})
	repo := "1.dkr.ecr.us-west-2.amazonaws.com/le-certs"
	old := "pkg:deb/debian/libbrotli1@1.0.9-2+b6?arch=amd64&os_version=12&upstream=brotli"
	_ = e.st.SetImageSBOM(ctx, sc, store.ImageSBOM{Repo: repo, Digest: "sha256:old", Purls: []string{old}, Source: "packages"})
	_ = e.st.SetImageSBOM(ctx, sc, store.ImageSBOM{Repo: repo, Digest: "sha256:new", Purls: []string{"pkg:deb/debian/base-files@13.8?arch=amd64&os_version=13"}, Source: "packages"})
	_ = e.st.SetImageEvidence(ctx, sc, store.ImageEvidence{Repo: repo, Digest: "sha256:old", CheckedAt: time.Now()})
	_ = e.st.SetImageEvidence(ctx, sc, store.ImageEvidence{Repo: repo, Digest: "sha256:new", CheckedAt: time.Now()})
	_ = e.st.SetPackageVulns(ctx, map[string][]string{old: {"DEBIAN-CVE-2026-111497"}, "pkg:deb/debian/base-files@13.8?arch=amd64&os_version=13": {}})
	_ = e.st.SetVuln(ctx, store.Vuln{ID: "DEBIAN-CVE-2026-111497", Aliases: []string{"CVE-2026-111497"}, Severity: "HIGH", Fixes: map[string][]string{"Debian:12|brotli": {"1.0.9-2+deb12u1"}}})
	_ = e.st.SetKnownExploited(ctx, []store.Exploited{{CVE: "CVE-2026-111497", DateAdded: time.Now().Format(time.DateOnly)}})

	viewer := e.as(store.RoleViewer)
	_, page := get(t, viewer, e.srv.URL+"/services/le-certs", nil)
	if !strings.Contains(page, ">1 exploited</span>") || !strings.Contains(page, "none known") {
		t.Error("the images do not say what is in each")
	}
	if !strings.Contains(page, ">"+prod.Name+" (v2.1.1)<") {
		t.Error("the vulnerability does not say which version has it")
	}
	if _, list := get(t, viewer, e.srv.URL+"/security/vulns?env=all&q="+repo+":v2.3", nil); strings.Contains(list, "CVE-2026-111497") {
		t.Error("the image's link lists another image's vulnerability")
	}
}
