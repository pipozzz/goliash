// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package versions

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/pipozzz/goliash/internal/store"
)

func TestSplitPackage(t *testing.T) {
	for in, want := range map[string]string{
		"nginx 1.27.1-1~bookworm":    "nginx|1.27.1",
		"libssl3 3.0.15-1~deb12u1":   "libssl3|3.0.15",
		"curl 7:8.5.0-2":             "curl|8.5.0",
		"zlib1g 1:1.2.13.dfsg-1":     "zlib1g|1.2.13.dfsg",
		"OpenSSL 3.3.2-r1":           "openssl|3.3.2",
		"nginx 1.22.1+dfsg-9+deb12u": "nginx|1.22.1",
	} {
		name, version := splitPackage(in)
		if got := name + "|" + version; got != want {
			t.Errorf("%s: %s, want %s", in, got, want)
		}
	}
}

// CVE-2023-44487 as NVD has it for nginx: affected from 1.9.5 up to 1.25.2.
var rapidReset = []store.UpstreamRange{
	{Product: "f5:nginx", StartIncl: "1.9.5", EndIncl: "1.25.2"},
	{Product: "f5:nginx_plus", StartIncl: "r25", EndExcl: "r29"},
}

func TestUpstreamFix(t *testing.T) {
	cases := []struct {
		pkg      string
		ranges   []store.UpstreamRange
		ok       bool
		affected bool
		sure     bool
		short    string
	}{
		// nginx.org's nginx in the official image, matched against Debian's advisory with no fix: past upstream's range.
		{"nginx 1.27.1-1~bookworm", rapidReset, true, false, true, "upstream: not affected (only up to 1.25.2)"},
		// Debian's own nginx is in the range: upstream fixed it later.
		{"nginx 1.22.1-9+deb12u1", rapidReset, true, true, false, "upstream nginx fixed it after 1.25.2"},
		// Just past "up to": NVD may have closed the range at the newest release of its time; not sure.
		{"nginx 1.25.4-1~bookworm", rapidReset, true, false, false, "upstream: not affected (only up to 1.25.2)"},
		// A fixed version named: past it is sure, before it says which version fixes it.
		{"nginx 1.25.3-1", []store.UpstreamRange{{Product: "f5:nginx", EndExcl: "1.25.3"}}, true, false, true, "upstream: not affected (fixed in 1.25.3)"},
		{"nginx 1.24.0-1", []store.UpstreamRange{{Product: "f5:nginx", StartIncl: "1.9.5", EndExcl: "1.25.3"}}, true, true, false, "upstream nginx fixed it in 1.25.3"},
		// Open-ended: no fix upstream either; nothing to say.
		{"nginx 1.27.1-1", []store.UpstreamRange{{Product: "f5:nginx", StartIncl: "1.0"}}, false, false, false, ""},
		// Another product's ranges, or a package Goliash knows no product of.
		{"nginx 1.27.1-1", []store.UpstreamRange{{Product: "f5:nginx_plus", EndExcl: "r29"}}, false, false, false, ""},
		{"libfoo 1.0.0-1", rapidReset, false, false, false, ""},
		// An exact version named.
		{"openssl 3.0.7-1", []store.UpstreamRange{{Product: "openssl:openssl", Exact: "3.0.7"}}, false, false, false, ""},
	}
	for _, c := range cases {
		u, ok := upstreamFix(c.ranges, c.pkg)
		if ok != c.ok || u.Affected != c.affected || u.Sure != c.sure || (ok && u.Short() != c.short) {
			t.Errorf("%s: %+v %v (%s)", c.pkg, u, ok, u.Short())
		}
	}
	u, _ := upstreamFix(rapidReset, "nginx 1.27.1-1~bookworm")
	if a := u.Advice("CVE-2023-44487"); !strings.Contains(a, "this runs nginx 1.27.1, past 1.25.2") {
		t.Errorf("advice: %s", a)
	}
	u, _ = upstreamFix([]store.UpstreamRange{{Product: "f5:nginx", StartIncl: "1.9.5", EndExcl: "1.25.3"}}, "nginx 1.24.0-1")
	if a := u.Advice("CVE-2023-44487"); !strings.Contains(a, "run nginx 1.25.3 or newer") {
		t.Errorf("advice: %s", a)
	}
}

func TestNVDRanges(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("cveId") != "CVE-2023-44487" {
			t.Errorf("query: %s", r.URL.RawQuery)
		}
		_, _ = w.Write([]byte(`{"totalResults":1,"vulnerabilities":[{"cve":{"id":"CVE-2023-44487","configurations":[{"nodes":[{"cpeMatch":[
			{"vulnerable":true,"criteria":"cpe:2.3:a:f5:nginx:*:*:*:*:*:*:*:*","versionStartIncluding":"1.9.5","versionEndIncluding":"1.25.2"},
			{"vulnerable":true,"criteria":"cpe:2.3:a:apple:swiftnio_http\/2:*:*:*:*:*:*:*:*","versionEndExcluding":"1.28.0"},
			{"vulnerable":true,"criteria":"cpe:2.3:a:openssl:openssl:3.0.7:*:*:*:*:*:*:*"},
			{"vulnerable":false,"criteria":"cpe:2.3:o:debian:debian_linux:12.0:*:*:*:*:*:*:*"}]}]}]}}]}`))
	}))
	defer srv.Close()
	n := NewNVD("key")
	n.BaseURL = srv.URL
	got, err := n.Ranges(context.Background(), "CVE-2023-44487")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != (store.UpstreamRange{Product: "f5:nginx", StartIncl: "1.9.5", EndIncl: "1.25.2"}) || got[1].Exact != "3.0.7" {
		t.Errorf("ranges: %+v", got)
	}
}
