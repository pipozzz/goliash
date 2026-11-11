// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package versions

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestOSVModified(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"results":[{"vulns":[{"id":"DEBIAN-CVE-2023-44487","modified":"2026-10-26T18:02:11.801128554Z"}]}]}`))
	}))
	defer srv.Close()
	o := &OSV{BaseURL: srv.URL, HTTP: srv.Client()}
	got, err := o.QueryBatch(context.Background(), []string{"pkg:deb/debian/nginx@1.22.1-9?os_version=12"})
	if err != nil || len(got["pkg:deb/debian/nginx@1.22.1-9?os_version=12"]) != 1 {
		t.Fatalf("%v %v", got, err)
	}
	if m := o.Modified("DEBIAN-CVE-2023-44487"); !m.Equal(time.Date(2026, 10, 26, 18, 2, 11, 801128554, time.UTC)) {
		t.Errorf("modified: %v", m)
	}
	if !o.Modified("other").IsZero() {
		t.Error("unknown record has a date")
	}
}
