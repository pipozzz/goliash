// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package versions

import (
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestRPMVerCmp(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want int
	}{
		{"1.0", "1.0", 0},
		{"1.0", "2.0", -1},
		{"2.0.1", "2.0", 1},
		{"1.10", "1.9", 1},
		{"1.0a", "1.0", 1},
		{"1.0~rc1", "1.0", -1},
		{"1.0^git1", "1.0", 1},
		{"1.0^git1", "1.0.1", -1},
		{"a", "1", -1},
		{"1", "a", 1},
		{"3.0.8", "3.0.8", 0},
		{"1.amzn2023.0.12", "1.amzn2023.0.9", 1},
		{"001", "1", 0},
		// as rpm itself answers (rpm.vercmp on Amazon Linux 2023)
		{"2.4.62", "2.4.62a", -1},
		{"5.14.0-503", "5.14.0-427", 1},
		{"1.0~rc1", "1.0~rc2", -1},
		{"1.0.0^", "1.0.0", 1},
	} {
		if got := rpmVerCmp(c.a, c.b); got != c.want {
			t.Errorf("%s vs %s: %d, want %d", c.a, c.b, got, c.want)
		}
	}
	if rpmCompare("1:3.0.8-1.amzn2023.0.12", "1:3.0.8-1.amzn2023.0.14") != -1 || rpmCompare("1:1.0-1", "0:9.0-1") != 1 {
		t.Error("epoch, version, release")
	}
}

func TestAmazonPurl(t *testing.T) {
	r, n, evr, ok := amazonPurl("pkg:rpm/amzn/openssl-libs@3.0.8-1.amzn2023.0.12?arch=x86_64&epoch=1&os_version=2023&upstream=openssl")
	if !ok || r != "2023" || n != "openssl-libs" || evr != "1:3.0.8-1.amzn2023.0.12" {
		t.Fatalf("%s %s %s %v", r, n, evr, ok)
	}
	if _, _, _, ok := amazonPurl("pkg:rpm/amzn/x@1-1?distro=amzn-2"); !ok {
		t.Error("an SBOM tool's distro=amzn-2")
	}
	if _, _, _, ok := amazonPurl("pkg:rpm/amzn/x@1-1?os_version=2018.03"); ok {
		t.Error("a release without advisories")
	}
	if _, _, _, ok := amazonPurl("pkg:rpm/rocky/x@1-1?os_version=9"); ok {
		t.Error("another distribution")
	}
}

const sampleUpdateinfo = `<?xml version="1.0" ?>
<updates>
<update type="security"><id>ALAS2023-2026-1001</id><title>Amazon Linux 2023 - ALAS2023-2026-1001: Important priority package update for openssl</title>
<severity>Important</severity><references><reference id="CVE-2026-1111" type="cve" /><reference id="CVE-2026-2222" type="cve" /></references>
<pkglist><collection><package name="openssl-libs" version="3.0.8" release="1.amzn2023.0.14" epoch="1" arch="x86_64"/>
<package name="openssl-libs" version="3.0.8" release="1.amzn2023.0.14" epoch="1" arch="aarch64"/>
<package name="openssl-debuginfo" version="3.0.8" release="1.amzn2023.0.14" epoch="1" arch="x86_64"/></collection></pkglist></update>
<update type="bugfix"><id>ALAS2023-2026-1002</id><pkglist><collection><package name="openssl-libs" version="9" release="1" epoch="9"/></collection></pkglist></update>
<update type="security"><id>ALAS2023-2026-1003</id><title>old curl</title><severity>Medium</severity><references><reference id="CVE-2025-1" type="cve" /></references>
<pkglist><collection><package name="curl" version="8.0.1" release="1.amzn2023" epoch="0"/></collection></pkglist></update>
</updates>`

// An Amazon Linux package older than an advisory's fixed version has its vulnerabilities, with the CVEs as aliases
// and the fix; one at or past it does not; a bug fix is no advisory.
func TestAmazonLinuxAdvisories(t *testing.T) {
	var gz bytes.Buffer
	w := gzip.NewWriter(&gz)
	_, _ = io.WriteString(w, sampleUpdateinfo)
	_ = w.Close()
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/mirror.list":
			_, _ = io.WriteString(rw, srv.URL+"/repo/\n")
		case "/repo/repodata/repomd.xml":
			_, _ = io.WriteString(rw, `<repomd><data type="primary"><location href="repodata/primary.xml.gz"/></data><data type="updateinfo"><location href="repodata/u.xml.gz"/></data></repomd>`)
		case "/repo/repodata/u.xml.gz":
			_, _ = rw.Write(gz.Bytes())
		default:
			http.NotFound(rw, r)
		}
	}))
	defer srv.Close()
	saved := alas
	alas = &alasCache{byRe: map[string]*alasIndex{}, mirrors: map[string]string{"2023": srv.URL + "/mirror.list"}, http: srv.Client()}
	defer func() { alas = saved }()

	l := newLab(t)
	l.checker = NewChecker(l.st, l.tags, slog.New(slog.NewTextHandler(io.Discard, nil)), time.Hour)
	old := "pkg:rpm/amzn/openssl-libs@3.0.8-1.amzn2023.0.12?arch=x86_64&epoch=1&os_version=2023&upstream=openssl"
	fixed := "pkg:rpm/amzn/openssl-libs@3.0.8-1.amzn2023.0.14?arch=x86_64&epoch=1&os_version=2023&upstream=openssl"
	curl := "pkg:rpm/amzn/curl@8.5.0-1.amzn2023.0.4?arch=x86_64&os_version=2023"
	got, err := l.checker.amazonLinuxVulns(context.Background(), []string{old, fixed, curl, "pkg:deb/debian/x@1?os_version=12"})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got[old], []string{"ALAS2023-2026-1001"}) || len(got[fixed]) != 0 || len(got[curl]) != 0 || len(got) != 3 {
		t.Fatalf("answers %v", got)
	}
	recs, _ := l.st.ListVulns(context.Background())
	rec := recs["ALAS2023-2026-1001"]
	if !slices.Contains(rec.Aliases, "CVE-2026-1111") || rec.Severity != "HIGH" || !strings.Contains(rec.Summary, "openssl") {
		t.Fatalf("record %+v", rec)
	}
	if fix := fixesFor(old, rec.Fixes); len(fix) != 1 || fix[0] != "1:3.0.8-1.amzn2023.0.14" {
		t.Fatalf("fix %v", fix)
	}
	if undercounted([]string{old}) != "" {
		t.Error("Amazon Linux 2023 still said to have no advisories")
	}
}
