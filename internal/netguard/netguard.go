// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

// Package netguard keeps the server's requests to addresses people type in (notification channels, ticket
// trackers, registries, release notes pages) away from the server itself and its cloud's metadata service, whose
// credentials are the server's. Private networks stay reachable by default: a self-hosted Goliash notifies an ntfy,
// a Mattermost or a Jira on the LAN. GOLIASH_PRIVATE_NETWORKS=deny refuses them too; =all lifts the guard.
package netguard

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"os"
	"syscall"
	"time"
)

// ErrRefused is the error of a connection to an address the guard refuses.
var ErrRefused = errors.New("refusing to connect to this address (loopback, link-local or cloud metadata; see GOLIASH_PRIVATE_NETWORKS)")

// metadata are cloud metadata addresses outside the link-local range.
var metadata = []netip.Prefix{
	netip.MustParsePrefix("fd00:ec2::254/128"), // AWS, over IPv6
}

// Allowed reports whether a connection to a may be made under the mode GOLIASH_PRIVATE_NETWORKS names.
func Allowed(a netip.Addr, mode string) bool {
	if mode == "all" {
		return true
	}
	a = a.Unmap()
	if !a.IsValid() || a.IsLoopback() || a.IsLinkLocalUnicast() || a.IsLinkLocalMulticast() || a.IsUnspecified() || a.IsMulticast() {
		return false
	}
	for _, p := range metadata {
		if p.Contains(a) {
			return false
		}
	}
	if mode == "deny" && a.IsPrivate() {
		return false
	}
	return true
}

// Transport is an HTTP transport that dials only addresses Allowed, checked at connection time (after DNS, and on
// every redirect).
func Transport() *http.Transport {
	dialer := &net.Dialer{
		Timeout: 10 * time.Second,
		Control: func(_, address string, _ syscall.RawConn) error {
			ap, err := netip.ParseAddrPort(address)
			if err != nil {
				return err
			}
			if !Allowed(ap.Addr(), os.Getenv("GOLIASH_PRIVATE_NETWORKS")) {
				return ErrRefused
			}
			return nil
		},
	}
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		return dialer.DialContext(ctx, network, addr)
	}
	return t
}

// Client is an HTTP client with the guarded Transport.
func Client(timeout time.Duration) *http.Client {
	return &http.Client{Timeout: timeout, Transport: Transport()}
}
