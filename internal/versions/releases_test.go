// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package versions

import (
	"strings"
	"testing"

	"github.com/pipozzz/goliash/internal/store"
)

func TestTagVersion(t *testing.T) {
	for tag, want := range map[string]string{
		"release-1.29.3": "1.29.3", "openssl-3.4.0": "3.4.0", "curl-8_11_0": "8.11.0", "v2.1.0": "2.1.0", "v1.3.1": "1.3.1",
	} {
		if v, ok := tagVersion(tag); !ok || versionNumber(v) != want {
			t.Errorf("%s: %v %v", tag, versionNumber(v), ok)
		}
	}
	if _, ok := tagVersion("nightly"); ok {
		t.Error("nightly is a version")
	}
}

func TestReleaseSources(t *testing.T) {
	if eol, gh := releaseSources("f5:nginx"); eol != "nginx" || gh != "nginx/nginx" {
		t.Errorf("nginx: %s %s", eol, gh)
	}
	if eol, gh := releaseSources("haxx:curl"); eol != "" || gh != "curl/curl" {
		t.Errorf("curl: %s %s", eol, gh)
	}
}

func TestUpstreamNewest(t *testing.T) {
	nginx := []store.ProductCycle{{Name: "1.31", Latest: "1.31.8"}, {Name: "1.30", Latest: "1.30.4"}, {Name: "1.24", Latest: "1.24.0", EOL: true}}
	// 1.30 runs, affected up to 1.30.1: the newest, and the newest of 1.30.
	u, ok := upstreamFix([]store.UpstreamRange{{Product: "f5:nginx", StartIncl: "1.30.0", EndExcl: "1.30.2"}}, "nginx 1.30.1-1~trixie")
	u.withReleases(nginx)
	if !ok || u.Latest != "1.31.8" || u.CycleLatest != "1.30.4" || u.Short() != "upstream nginx fixed it in 1.30.2 · latest 1.31.8" ||
		!strings.Contains(u.Advice("CVE-1"), "The newest is 1.31.8; on 1.30, 1.30.4.") {
		t.Errorf("1.30: %+v %s", u, u.Advice("CVE-1"))
	}
	// 1.24 is past its end of life: only the newest overall.
	u, _ = upstreamFix([]store.UpstreamRange{{Product: "f5:nginx", EndIncl: "1.25.2"}}, "nginx 1.24.0-1")
	u.withReleases(nginx)
	if u.Latest != "1.31.8" || u.CycleLatest != "" || !strings.HasSuffix(u.Advice("CVE-1"), "The newest is 1.31.8.") {
		t.Errorf("1.24: %+v", u)
	}
	// A newest release that does not fix it is not offered.
	u, _ = upstreamFix([]store.UpstreamRange{{Product: "f5:nginx", EndExcl: "1.32.0"}}, "nginx 1.31.1")
	u.withReleases(nginx)
	if u.Latest != "" || u.Short() != "upstream nginx fixed it in 1.32.0" {
		t.Errorf("unfixed newest: %+v", u)
	}
}
