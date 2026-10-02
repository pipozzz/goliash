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
