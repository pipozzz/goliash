// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package ui

import (
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCompress(t *testing.T) {
	page := strings.Repeat("<tr><td>payments-api</td><td>1.6.0</td></tr>", 500)
	h := Compress(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/page":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = io.WriteString(w, page)
		case "/small":
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Content-Length", "2")
			_, _ = io.WriteString(w, "{}")
		case "/png":
			w.Header().Set("Content-Type", "image/png")
			_, _ = io.WriteString(w, strings.Repeat("x", 5000))
		case "/stream":
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "data: hi\n\n")
			w.(http.Flusher).Flush()
		case "/detect":
			_, _ = io.WriteString(w, "<!doctype html>"+page)
		}
	}))
	get := func(path, accept string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		if accept != "" {
			req.Header.Set("Accept-Encoding", accept)
		}
		h.ServeHTTP(rec, req)
		return rec
	}

	rec := get("/page", "gzip, deflate, br")
	if rec.Header().Get("Content-Encoding") != "gzip" || rec.Body.Len() > len(page)/10 {
		t.Fatalf("page not compressed: %v, %d of %d bytes", rec.Header(), rec.Body.Len(), len(page))
	}
	zr, err := gzip.NewReader(rec.Body)
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := io.ReadAll(zr); string(b) != page {
		t.Fatal("round trip")
	}
	if !strings.Contains(rec.Header().Get("Vary"), "Accept-Encoding") {
		t.Error("no Vary")
	}
	for _, c := range []struct{ path, accept string }{
		{"/page", ""}, {"/page", "br"}, {"/page", "gzip;q=0"}, {"/small", "gzip"}, {"/png", "gzip"}, {"/stream", "gzip"},
	} {
		if rec := get(c.path, c.accept); rec.Header().Get("Content-Encoding") != "" {
			t.Errorf("%s with %q compressed", c.path, c.accept)
		}
	}
	if rec := get("/stream", "gzip"); rec.Body.String() != "data: hi\n\n" || !rec.Flushed {
		t.Error("event stream changed or not flushed")
	}
	if rec := get("/detect", "gzip"); rec.Header().Get("Content-Encoding") != "gzip" {
		t.Error("a body without a content type was not detected as HTML")
	}
}
