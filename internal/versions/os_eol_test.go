// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package versions

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/pipozzz/goliash/internal/store"
)

func TestOSOf(t *testing.T) {
	for _, c := range []struct {
		purls []string
		want  ImageOS
		ok    bool
	}{
		{[]string{"pkg:npm/express@4.19.2", "pkg:deb/debian/libc6@2.31-13?arch=amd64&os_version=11"}, ImageOS{"debian", "11"}, true},
		{[]string{"pkg:deb/debian/libc6@2.36?distro=debian-12.7"}, ImageOS{"debian", "12"}, true},
		{[]string{"pkg:apk/alpine/musl@1.2.4?os_version=3.18.6"}, ImageOS{"alpine", "3.18.6"}, true},
		{[]string{"pkg:rpm/redhat/openssl-libs@3.0.7?distro=rhel-9.4"}, ImageOS{"redhat", "9.4"}, true},
		{[]string{"pkg:apk/wolfi/glibc@2.40"}, ImageOS{}, false},
		{[]string{"pkg:golang/golang.org/x/net@v0.30.0"}, ImageOS{}, false},
	} {
		got, ok := OSOf(c.purls)
		if got != c.want || ok != c.ok {
			t.Errorf("OSOf(%v) = %v %v, want %v %v", c.purls, got, ok, c.want, c.ok)
		}
	}
	if s := (ImageOS{"debian", "11"}).String(); s != "Debian 11 (bullseye)" {
		t.Errorf("name %q", s)
	}
}

func TestEOLOperatingSystems(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/products/debian":
			_, _ = w.Write([]byte(`{"result":{"releases":[{"name":"13","isEol":false,"eolFrom":"2030-06-30"},{"name":"12","isEol":false,"eolFrom":"2028-06-30"},{"name":"11","isEol":true,"eolFrom":"2026-08-31"}]}}`))
		case "/products/alpine-linux":
			_, _ = w.Write([]byte(`{"result":{"releases":[{"name":"3.24","isEol":false,"eolFrom":"2028-06-01"},{"name":"3.21","isEol":false,"eolFrom":"2026-12-01"}]}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	e := NewEOL()
	e.BaseURL = srv.URL
	(&Checker{}).SetEOL(e)
	defer sharedEOL.Store(nil)
	ctx := context.Background()
	for _, p := range []string{"debian", "alpine-linux"} { // as the background refresh would
		if _, err := e.Cycles(ctx, p); err != nil {
			t.Fatal(err)
		}
	}

	now := time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC)
	active := []store.Instance{
		{Image: "ghcr.io/acme/api:1.0", Digest: "sha256:a", EnvironmentID: "e", TargetID: "t", WorkloadName: "api", ContainerName: "api"},
		{Image: "ghcr.io/acme/web:2.0", Digest: "sha256:b", EnvironmentID: "e", TargetID: "t", WorkloadName: "web", ContainerName: "web"},
		{Image: "ghcr.io/acme/new:3.0", Digest: "sha256:c", EnvironmentID: "e", TargetID: "t", WorkloadName: "new", ContainerName: "new"},
	}
	sboms := map[string]store.ImageSBOM{
		"ghcr.io/acme/api@sha256:a": {Purls: []string{"pkg:deb/debian/libc6@2.31?os_version=11"}},
		"ghcr.io/acme/web@sha256:b": {Purls: []string{"pkg:apk/alpine/musl@1.2.5?os_version=3.21.2"}},
		"ghcr.io/acme/new@sha256:c": {Purls: []string{"pkg:deb/debian/libc6@2.41?os_version=13"}},
	}
	got := EOLOperatingSystems(ctx, active, sboms, map[string]string{"e": "prod"}, map[string]string{"t": "eks"}, now)
	if len(got) != 2 {
		t.Fatalf("findings %+v", got)
	}
	if f := got[0]; f.Image != "ghcr.io/acme/api:1.0" || f.Severity != "warning" ||
		f.Detail != "built on Debian 11 (bullseye), past its end of life since Aug 2026: its packages get no more security fixes; rebuild on Debian 13 (trixie)" {
		t.Fatalf("past %+v", f)
	}
	if f := got[1]; f.Severity != "info" || !strings.Contains(f.Detail, "Alpine 3.21, whose end of life is Dec 1, 2026") || !strings.HasSuffix(f.Detail, "rebuild on Alpine 3.24") {
		t.Fatalf("soon %+v", f)
	}
}
