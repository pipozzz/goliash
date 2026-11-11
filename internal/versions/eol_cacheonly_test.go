// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package versions

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// A page does not wait for endoflife.date: a cache-only lookup answers at once, and the next one has what the
// background fetch brought.
func TestEOLCacheOnly(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(300 * time.Millisecond) // a slow site
		_, _ = w.Write([]byte(`{"result":{"releases":[{"name":"3.12","isEol":false,"eolFrom":"2028-10-31"}]}}`))
	}))
	defer srv.Close()
	e := NewEOL()
	e.BaseURL = srv.URL
	start := time.Now()
	if _, err := e.Cycles(CacheOnly(context.Background()), "python"); !errors.Is(err, ErrNotCached) {
		t.Fatalf("cold: %v", err)
	}
	if time.Since(start) > 100*time.Millisecond {
		t.Fatal("a cache-only lookup waited for the site")
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		cycles, err := e.Cycles(CacheOnly(context.Background()), "python")
		if err == nil && len(cycles) == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the background fetch did not fill the cache")
		}
		time.Sleep(20 * time.Millisecond)
	}
}
