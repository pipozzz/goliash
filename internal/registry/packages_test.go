// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: Apache-2.0

package registry

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// layer is a gzip tar of files: path -> content ("" with a .wh. name is a whiteout).
func layer(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range files {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		_, _ = tw.Write([]byte(body))
	}
	_ = tw.Close()
	_ = gz.Close()
	return buf.Bytes()
}

func packagesServer(t *testing.T, layers ...[]byte) *httptest.Server {
	t.Helper()
	blobs := map[string][]byte{}
	var descs []string
	for i, l := range layers {
		d := "sha256:l" + string(rune('0'+i))
		blobs["/v2/acme/app/blobs/"+d] = l
		descs = append(descs, `{"digest":"`+d+`","size":`+itoaTest(len(l))+`}`)
	}
	manifest := `{"mediaType":"` + mediaManifest + `","config":{"digest":"sha256:cfg"},"layers":[` + strings.Join(descs, ",") + `]}`
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v2/acme/app/manifests/sha256:img" {
			_, _ = w.Write([]byte(manifest))
			return
		}
		if b, ok := blobs[r.URL.Path]; ok {
			_, _ = w.Write(b)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
}

func itoaTest(n int) string {
	var b []byte
	for {
		b = append([]byte{byte('0' + n%10)}, b...)
		if n /= 10; n == 0 {
			return string(b)
		}
	}
}

const ubuntuStatus = `Package: nginx
Status: install ok installed
Architecture: amd64
Version: 1.18.0-6ubuntu14.4
Source: nginx

Package: libssl3
Status: install ok installed
Architecture: amd64
Source: openssl (3.0.2-0ubuntu1.15)
Version: 3.0.2-0ubuntu1.15

Package: removed-thing
Status: deinstall ok config-files
Version: 1.0
`

func TestImagePackagesUbuntu(t *testing.T) {
	// Ubuntu at the bottom, nginx and php installed with apt in the layer above, the app on top.
	srv := packagesServer(t,
		layer(t, map[string]string{"usr/lib/os-release": "ID=ubuntu\nVERSION_ID=\"22.04\"\n", "var/lib/dpkg/status": "Package: base-files\nStatus: install ok installed\nVersion: 12ubuntu4\n"}),
		layer(t, map[string]string{"var/lib/dpkg/status": ubuntuStatus}),
		layer(t, map[string]string{"app/index.php": "<?php"}),
	)
	defer srv.Close()
	host := strings.TrimPrefix(srv.URL, "http://")
	sbom, err := client().ImagePackages(context.Background(), host+"/acme/app", "sha256:img", Credentials{})
	if err != nil {
		t.Fatal(err)
	}
	want := "pkg:deb/ubuntu/libssl3@3.0.2-0ubuntu1.15?arch=amd64&os_version=22.04&upstream=openssl " +
		"pkg:deb/ubuntu/nginx@1.18.0-6ubuntu14.4?arch=amd64&os_version=22.04"
	if got := strings.Join(sbom.Purls, " "); got != want {
		t.Errorf("purls:\n%s\nwant\n%s", got, want)
	}
}

func TestImagePackagesAlpine(t *testing.T) {
	srv := packagesServer(t,
		layer(t, map[string]string{"etc/os-release": "ID=alpine\nVERSION_ID=3.19.1\n", "lib/apk/db/installed": "P:musl\nV:1.2.4-r2\nA:x86_64\no:musl\n\n"}),
		layer(t, map[string]string{"lib/apk/db/installed": "P:musl\nV:1.2.4-r2\nA:x86_64\no:musl\n\nP:nginx\nV:1.24.0-r15\nA:x86_64\no:nginx\n\nP:php82-fpm\nV:8.2.20-r0\nA:x86_64\no:php82\n"}),
	)
	defer srv.Close()
	host := strings.TrimPrefix(srv.URL, "http://")
	sbom, err := client().ImagePackages(context.Background(), host+"/acme/app", "sha256:img", Credentials{})
	if err != nil {
		t.Fatal(err)
	}
	want := "pkg:apk/alpine/musl@1.2.4-r2?arch=x86_64&os_version=3.19.1 pkg:apk/alpine/nginx@1.24.0-r15?arch=x86_64&os_version=3.19.1 " +
		"pkg:apk/alpine/php82-fpm@8.2.20-r0?arch=x86_64&origin=php82&os_version=3.19.1"
	if got := strings.Join(sbom.Purls, " "); got != want {
		t.Errorf("purls:\n%s\nwant\n%s", got, want)
	}
}

func TestImagePackagesNone(t *testing.T) {
	srv := packagesServer(t, layer(t, map[string]string{"app": "binary"}))
	defer srv.Close()
	host := strings.TrimPrefix(srv.URL, "http://")
	if _, err := client().ImagePackages(context.Background(), host+"/acme/app", "sha256:img", Credentials{}); !errors.Is(err, ErrNoPackageDatabase) {
		t.Errorf("scratch image: %v", err)
	}
}
