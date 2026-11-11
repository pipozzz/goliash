// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: Apache-2.0

package registry

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Some image names use a registry host of their own that only redirects to a public registry: docker.flagsmith.com
// answers every /v2/ request with a redirect to registry-1.docker.io. Such a host is an alias: its images are read
// from the public registry it points to.
var aliases sync.Map // host -> public registry host, as image names write it (docker.io, ghcr.io …)

// publicEndpoints are the API hosts of public registries an alias may point to, with the host image names use.
var publicEndpoints = map[string]string{
	"registry-1.docker.io": "docker.io", "index.docker.io": "docker.io", "ghcr.io": "ghcr.io", "quay.io": "quay.io",
	"registry.gitlab.com": "registry.gitlab.com", "public.ecr.aws": "public.ecr.aws", "gcr.io": "gcr.io",
	"mcr.microsoft.com": "mcr.microsoft.com", "registry.k8s.io": "registry.k8s.io",
}

// Alias returns the public registry host stands for, when it is an alias found by ProbeAlias.
func Alias(host string) (string, bool) {
	v, ok := aliases.Load(host)
	if !ok {
		return "", false
	}
	return v.(string), true
}

// ProbeAlias asks host's /v2/ (anonymously, over HTTPS, on public addresses only, following no redirect) whether it
// redirects to a public registry, and remembers it as an alias if so.
func ProbeAlias(ctx context.Context, host string) (string, bool) {
	if host == "" || strings.ContainsAny(host, "/@?#") {
		return "", false
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+host+"/v2/", nil)
	if err != nil {
		return "", false
	}
	resp, err := probeClient.Do(req)
	if err != nil {
		return "", false
	}
	_ = resp.Body.Close()
	if resp.StatusCode < 300 || resp.StatusCode > 399 {
		return "", false
	}
	loc, err := url.Parse(resp.Header.Get("Location"))
	if err != nil || loc.Scheme != "https" || strings.TrimSuffix(loc.Path, "/") != "/v2" {
		return "", false
	}
	public, ok := publicEndpoints[loc.Hostname()]
	if !ok {
		return "", false
	}
	aliases.Store(host, public)
	return public, true
}

// probeClient reaches the internet only: image names come from what runs, so a name must not make the server call
// into its own network.
var probeClient = &http.Client{
	Timeout: 10 * time.Second,
	Transport: &http.Transport{
		DialContext: (&net.Dialer{
			Timeout: 5 * time.Second,
			Control: func(_, address string, _ syscall.RawConn) error {
				ap, err := netip.ParseAddrPort(address)
				if err != nil {
					return err
				}
				if a := ap.Addr().Unmap(); !a.IsGlobalUnicast() || a.IsPrivate() {
					return errors.New("refusing to connect to a non-public address")
				}
				return nil
			},
		}).DialContext,
		TLSHandshakeTimeout:   5 * time.Second,
		ResponseHeaderTimeout: 5 * time.Second,
	},
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}
