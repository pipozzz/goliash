// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package versions

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/pipozzz/goliash/internal/store"
)

// A distribution's own record that plans no security update for a release (Debian's unimportant, Ubuntu's
// negligible) is kept with its fixes, so the package it covers leaves the findings with that reason.
func TestDistroRatings(t *testing.T) {
	records := map[string]string{
		// CVE-2026-123158: sshd with musl and no PAM (Alpine); Debian's glibc build is not affected in practice.
		"DEBIAN-CVE-2026-123158": `{"id":"DEBIAN-CVE-2026-123158","affected":[
			{"package":{"name":"openssh","ecosystem":"Debian:13"},"ranges":[{"events":[{"introduced":"0"}]}],"ecosystem_specific":{"urgency":"unimportant"}},
			{"package":{"name":"openssh","ecosystem":"Debian:14"},"ranges":[{"events":[{"introduced":"0"},{"fixed":"1:10.6p2-1"}]}],"ecosystem_specific":{"urgency":"unimportant"}},
			{"package":{"name":"curl","ecosystem":"Debian:13"},"ranges":[{"events":[{"introduced":"0"}]}],"ecosystem_specific":{"urgency":"high"}}]}`,
		"UBUNTU-CVE-2026-1": `{"id":"UBUNTU-CVE-2026-1","severity":[{"type":"Ubuntu","score":"negligible"}],"affected":[
			{"package":{"name":"tar","ecosystem":"Ubuntu:24.04:LTS"},"ranges":[{"events":[{"introduced":"0"}]}]}]}`,
		"UBUNTU-CVE-2026-2": `{"id":"UBUNTU-CVE-2026-2","severity":[{"type":"Ubuntu","score":"high"}],"affected":[
			{"package":{"name":"openssh","ecosystem":"Ubuntu:24.04:LTS"},"ranges":[{"events":[{"introduced":"0"}]}]}]}`,
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, records[strings.TrimPrefix(r.URL.Path, "/vulns/")])
	}))
	defer srv.Close()
	o := NewOSV(srv.URL)
	ctx := context.Background()
	deb, err := o.Vuln(ctx, "DEBIAN-CVE-2026-123158")
	if err != nil {
		t.Fatal(err)
	}
	trixie := "pkg:deb/debian/openssh-server@1:10.0p1-7+deb13u4?arch=amd64&os_version=13&upstream=openssh"
	if got := minorFor(trixie, deb.Fixes); got != DistroUnimportant {
		t.Errorf("trixie openssh: %q", got)
	}
	if got := minorFor("pkg:deb/debian/curl@8.14.1-2?arch=amd64&os_version=13", deb.Fixes); got != "" {
		t.Errorf("a high urgency is no minor issue: %q", got)
	}
	if deb.Fixes[fixesFormat] == nil {
		t.Error("the record is not marked as read with the ratings")
	}
	if a := (Affected{Package: "openssh-server 1:10.0p1-7+deb13u4", Distro: DistroUnimportant}); !strings.Contains(autoNotAffected(a), "Debian rates it unimportant") {
		t.Errorf("reason %q", autoNotAffected(a))
	}
	neg, _ := o.Vuln(ctx, "UBUNTU-CVE-2026-1")
	if got := minorFor("pkg:deb/ubuntu/tar@1.35+dfsg-3build1?arch=amd64&os_version=24.04", neg.Fixes); got != DistroNegligible {
		t.Errorf("ubuntu negligible: %q", got)
	}
	high, _ := o.Vuln(ctx, "UBUNTU-CVE-2026-2")
	if got := minorFor("pkg:deb/ubuntu/openssh-server@1:9.6p1-3ubuntu13?arch=amd64&os_version=24.04&upstream=openssh", high.Fixes); got != "" {
		t.Errorf("ubuntu high: %q", got)
	}
}

// Ubuntu files fixes of its LTS releases as Ubuntu:24.04:LTS; an image's packages say 24.04. The fix must be found.
func TestUbuntuLTSFix(t *testing.T) {
	fixes := map[string][]string{"Ubuntu:24.04:LTS|openssh": {"1:9.6p1-3ubuntu13.5"}}
	if got := fixesFor("pkg:deb/ubuntu/openssh-server@1:9.6p1-3ubuntu13?arch=amd64&os_version=24.04&upstream=openssh", fixes); len(got) != 1 {
		t.Fatalf("fix not found: %v", got)
	}
}

// A Go advisory names the packages its vulnerable code is in; an image whose Go programs link none of them is not
// affected, one that links one is, and an image whose programs were not read says nothing.
func TestGoUnlinked(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"id":"GO-2026-6859","aliases":["CVE-2026-9"],"affected":[{"package":{"name":"stdlib","ecosystem":"Go"},
			"ranges":[{"events":[{"introduced":"0"},{"fixed":"1.26.11"}]}],
			"ecosystem_specific":{"imports":[{"path":"net/http","symbols":["MaxBytesReader"]}]}}]}`)
	}))
	defer srv.Close()
	v, err := NewOSV(srv.URL).Vuln(context.Background(), "GO-2026-6859")
	if err != nil {
		t.Fatal(err)
	}
	stdlib := "pkg:golang/stdlib@1.26.10"
	imports := importsFor(stdlib, v.Fixes)
	if len(imports) != 1 || imports[0] != "net/http" {
		t.Fatalf("imports %v", imports)
	}
	if got := unlinked(imports, []string{"fmt", "os", "strings"}); got != "net/http" {
		t.Errorf("a CLI without net/http: %q", got)
	}
	if got := unlinked(imports, []string{"fmt", "net/http", "os"}); got != "" {
		t.Errorf("a server with net/http: %q", got)
	}
	if got := unlinked(imports, nil); got != "" {
		t.Errorf("programs not read: %q", got)
	}
	if got := unlinked(nil, []string{"fmt"}); got != "" {
		t.Errorf("an advisory naming no package: %q", got)
	}
	a := Affected{Package: "stdlib 1.26.10", GoUnlinked: "net/http"}
	if !strings.Contains(autoNotAffected(a), "do not link net/http") {
		t.Errorf("reason %q", autoNotAffected(a))
	}
}

// End to end: a Go advisory about net/http leaves the finding of an image whose programs do not link it, and stays
// for one whose programs do.
func TestGoUnlinkedReport(t *testing.T) {
	l := newLab(t)
	ctx := context.Background()
	tgt := l.targets["prod-a"]
	stdlib := "pkg:golang/stdlib@1.26.10"
	cli, _ := l.st.EnsureService(ctx, l.sc, "cli")
	if err := l.st.ApplySnapshot(ctx, store.SnapshotChanges{Scope: l.sc, TargetID: tgt.ID, SnapshotID: store.NewID(), At: time.Now(), Upsert: []store.Instance{
		{
			TargetID: tgt.ID, EnvironmentID: tgt.EnvironmentID, ServiceID: cli.ID, WorkloadID: "c", WorkloadName: "cli", ContainerName: "cli",
			Image: "example.com/cli:1", Tag: "1", Digest: "sha256:c", Running: 1, IsMain: true,
		},
		{
			TargetID: tgt.ID, EnvironmentID: tgt.EnvironmentID, ServiceID: l.svc.ID, WorkloadID: "w", WorkloadName: "web", ContainerName: "web",
			Image: "example.com/web:1", Tag: "1", Digest: "sha256:w", Running: 1, IsMain: true,
		},
	}}); err != nil {
		t.Fatal(err)
	}
	_ = l.st.SetImageSBOM(ctx, l.sc, store.ImageSBOM{Repo: "example.com/cli", Digest: "sha256:c", Purls: []string{stdlib}, Source: "packages", GoLinked: []string{"fmt", "os"}})
	_ = l.st.SetImageSBOM(ctx, l.sc, store.ImageSBOM{Repo: "example.com/web", Digest: "sha256:w", Purls: []string{stdlib}, Source: "packages", GoLinked: []string{"fmt", "net/http"}})
	_ = l.st.SetPackageVulns(ctx, map[string][]string{stdlib: {"GO-2026-6859"}})
	_ = l.st.SetVuln(ctx, store.Vuln{ID: "GO-2026-6859", Aliases: []string{"CVE-2026-9"}, Fixes: map[string][]string{
		"Go|stdlib": {"1.26.11"}, importsKey + "Go|stdlib": {"net/http"}, fixesFormat: {fixesFormatNow},
	}})
	r, err := LoadVulnReport(ctx, l.st, l.sc, "")
	if err != nil {
		t.Fatal(err)
	}
	var services []string
	for _, f := range r.Findings {
		for _, a := range f.Affected {
			services = append(services, a.Service)
		}
	}
	if len(services) != 1 || services[0] != "web" {
		t.Fatalf("affected %v", services)
	}
	if len(r.NotAffected) != 1 || r.NotAffected[0].Service != "cli" || !strings.Contains(r.NotAffected[0].Auto, "do not link net/http") {
		t.Fatalf("not affected %+v", r.NotAffected)
	}
}

// A kernel package in a container image is the host's business; on a host, it is the kernel that runs.
func TestKernelOnHost(t *testing.T) {
	image := Affected{Package: "linux-image-6.8.0-1015-aws 6.8.0-1015.16"}
	if autoNotAffected(image) == "" {
		t.Error("an image's kernel package kept")
	}
	host := image
	host.Host = true
	if why := autoNotAffected(host); why != "" {
		t.Errorf("a host's running kernel left out: %s", why)
	}
}
