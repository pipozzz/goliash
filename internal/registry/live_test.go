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

// Run with GOLIASH_LIVE_REGISTRY_TEST=1 to look for signatures and attestations of real images.
func TestLiveImageEvidence(t *testing.T) {
	if os.Getenv("GOLIASH_LIVE_REGISTRY_TEST") == "" {
		t.Skip("set GOLIASH_LIVE_REGISTRY_TEST=1")
	}
	for _, c := range []struct {
		repo, tag          string
		signed, sbom, prov bool
	}{
		{"docker.io/library/nginx", "1.27.3", false, true, true},
		{"ghcr.io/sigstore/cosign/cosign", "v2.4.1", true, false, false},
	} {
		digest, err := New().TagDigest(context.Background(), c.repo, c.tag, Credentials{})
		if err != nil {
			t.Fatal(err)
		}
		ev, err := New().ImageEvidence(context.Background(), c.repo, digest, Credentials{})
		t.Logf("%s:%s %s: %+v %v", c.repo, c.tag, digest, ev, err)
		if err != nil || (c.signed && !ev.Signed) || (c.sbom && !ev.SBOM) || (c.prov && !ev.Provenance) {
			t.Errorf("%s: %+v %v", c.repo, ev, err)
		}
	}
}

// Run with GOLIASH_LIVE_REGISTRY_TEST=1 to read the SBOM of a real image.
func TestLiveImageSBOM(t *testing.T) {
	if os.Getenv("GOLIASH_LIVE_REGISTRY_TEST") == "" {
		t.Skip("set GOLIASH_LIVE_REGISTRY_TEST=1")
	}
	digest, err := New().TagDigest(context.Background(), "docker.io/library/nginx", "1.27.3", Credentials{})
	if err != nil {
		t.Fatal(err)
	}
	purls, err := New().ImageSBOM(context.Background(), "docker.io/library/nginx", digest, Credentials{})
	t.Logf("%d purls, e.g. %v", len(purls), purls[:min(5, len(purls))])
	if err != nil || len(purls) < 20 {
		t.Fatalf("%d purls: %v", len(purls), err)
	}
}
