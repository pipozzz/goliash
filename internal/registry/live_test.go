// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: Apache-2.0

package registry

import (
	"context"
	"os"
	"testing"
)

// Run with GOLIASH_LIVE_REGISTRY_TEST=1 to list tags from real public registries.
func TestLivePublicRegistries(t *testing.T) {
	if os.Getenv("GOLIASH_LIVE_REGISTRY_TEST") == "" {
		t.Skip("set GOLIASH_LIVE_REGISTRY_TEST=1")
	}
	for _, repo := range []string{"docker.io/library/nginx", "ghcr.io/fluxcd/flux-cli", "quay.io/prometheus/node-exporter", "registry.k8s.io/coredns/coredns"} {
		tags, err := New().ListTags(context.Background(), repo, Credentials{})
		if err != nil {
			t.Errorf("%s: %v", repo, err)
			continue
		}
		t.Logf("%s: %d tags, e.g. %v", repo, len(tags), tags[len(tags)-min(3, len(tags)):])
	}
}

// Run with GOLIASH_LIVE_REGISTRY_TEST=1 to read source labels from real images.
func TestLiveImageSource(t *testing.T) {
	if os.Getenv("GOLIASH_LIVE_REGISTRY_TEST") == "" {
		t.Skip("set GOLIASH_LIVE_REGISTRY_TEST=1")
	}
	for repo, tag := range map[string]string{
		"docker.io/library/traefik":             "v3.1.2",
		"ghcr.io/fluxcd/flux-cli":               "v2.4.0",
		"quay.io/prometheus/node-exporter":      "v1.8.2",
		"docker.io/grafana/grafana":             "11.2.0",
		"docker.io/library/nginx":               "1.27.2",
		"ghcr.io/home-assistant/home-assistant": "stable",
	} {
		src, err := New().ImageSource(context.Background(), repo, tag, Credentials{})
		t.Logf("%s:%s -> %q (github %q) err=%v", repo, tag, src, GitHubRepository(src), err)
	}
}
