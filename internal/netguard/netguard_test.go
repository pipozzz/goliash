// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package netguard

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
)

func TestAllowed(t *testing.T) {
	for _, c := range []struct {
		addr, mode string
		want       bool
	}{
		{"169.254.169.254", "", false},
		{"fd00:ec2::254", "", false},
		{"127.0.0.1", "", false},
		{"::1", "", false},
		{"10.0.0.5", "", true},
		{"192.168.1.2", "", true},
		{"10.0.0.5", "deny", false},
		{"93.184.216.34", "deny", true},
		{"127.0.0.1", "all", true},
		{"0.0.0.0", "", false},
	} {
		if got := Allowed(netip.MustParseAddr(c.addr), c.mode); got != c.want {
			t.Errorf("%s under %q: %v", c.addr, c.mode, got)
		}
	}
}

// A request to the server's own loopback is refused at connection time.
func TestClientRefusesLoopback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer srv.Close()
	if _, err := Client(0).Get(srv.URL); err == nil {
		t.Fatal("reached a loopback address")
	}
	t.Setenv("GOLIASH_PRIVATE_NETWORKS", "all")
	resp, err := Client(0).Get(srv.URL)
	if err != nil {
		t.Fatalf("with the guard lifted: %v", err)
	}
	_ = resp.Body.Close()
}
