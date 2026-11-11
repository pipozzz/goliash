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
