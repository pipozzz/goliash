// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: Apache-2.0

package registry

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// A layer whose body takes longer than the client's timeout is read whole: its context bounds it, not the client.
func TestLongBodyOutlastsClientTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		for range 5 {
			_, _ = w.Write([]byte("chunk"))
			w.(http.Flusher).Flush()
			time.Sleep(60 * time.Millisecond)
		}
	}))
	defer srv.Close()
	c := New()
	c.HTTP.Timeout = 100 * time.Millisecond
	read := func(ctx context.Context) error {
		resp, err := c.do(ctx, http.MethodGet, srv.URL, "", Credentials{}, "*/*")
		if err != nil {
			return err
		}
		defer func() { _ = resp.Body.Close() }()
		_, err = io.ReadAll(resp.Body)
		return err
	}
	if err := read(context.Background()); err == nil {
		t.Fatal("the client's timeout no longer applies to ordinary requests")
	}
	if err := read(context.WithValue(context.Background(), longBody{}, true)); err != nil {
		t.Fatalf("a long body cut off: %v", err)
	}
}
