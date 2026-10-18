// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package versions

import (
	"strings"
	"testing"
)

func TestBumpImageTag(t *testing.T) {
	const checkout = "ghcr.io/acme/checkout"
	for _, c := range []struct {
		name, repo, in, want string
		changes              int
		pinned               bool
	}{
		{
			"manifest", checkout,
			"containers:\n  - name: app\n    image: ghcr.io/acme/checkout:1.4.2\n  - name: proxy\n    image: envoyproxy/envoy:1.4.2\n",
			"containers:\n  - name: app\n    image: ghcr.io/acme/checkout:1.5.0\n  - name: proxy\n    image: envoyproxy/envoy:1.4.2\n", 1, false,
		},
		{
			"quoted and docker hub short name", "docker.io/library/postgres",
			"image: \"postgres:15.6\"\nother: \"postgres:15.6-alpine\"\n", "image: \"postgres:15.8\"\nother: \"postgres:15.6-alpine\"\n", 1, false,
		},
		{
			"pinned by digest is left", checkout,
			"image: ghcr.io/acme/checkout:1.4.2@sha256:" + strings.Repeat("a", 64) + "\n",
			"image: ghcr.io/acme/checkout:1.4.2@sha256:" + strings.Repeat("a", 64) + "\n", 0, true,
		},
		{
			"kustomize images", checkout,
			"images:\n  - name: ghcr.io/acme/web\n    newTag: 1.4.2\n  - name: ghcr.io/acme/checkout\n    newTag: \"1.4.2\" # pinned for Q4\n",
			"images:\n  - name: ghcr.io/acme/web\n    newTag: 1.4.2\n  - name: ghcr.io/acme/checkout\n    newTag: \"1.5.0\" # pinned for Q4\n", 1, false,
		},
		{
			"kustomize newTag before name", checkout,
			"images:\n  - newTag: 1.4.2\n    name: ghcr.io/acme/checkout\n",
			"images:\n  - newTag: 1.5.0\n    name: ghcr.io/acme/checkout\n", 1, false,
		},
		{
			"helm values", checkout,
			"replicas: 2\nimage:\n  repository: ghcr.io/acme/checkout\n  pullPolicy: IfNotPresent\n  tag: '1.4.2'\nworker:\n  image:\n    repository: ghcr.io/acme/worker\n    tag: '1.4.2'\n",
			"replicas: 2\nimage:\n  repository: ghcr.io/acme/checkout\n  pullPolicy: IfNotPresent\n  tag: '1.5.0'\nworker:\n  image:\n    repository: ghcr.io/acme/worker\n    tag: '1.4.2'\n", 1, false,
		},
		{"another version is not touched", checkout, "image: ghcr.io/acme/checkout:1.3.0\n", "image: ghcr.io/acme/checkout:1.3.0\n", 0, false},
	} {
		got := BumpImageTag([]byte(c.in), c.repo, map[bool]string{true: "15.6", false: "1.4.2"}[c.repo == "docker.io/library/postgres"],
			map[bool]string{true: "15.8", false: "1.5.0"}[c.repo == "docker.io/library/postgres"])
		if string(got.Content) != c.want || got.Changes != c.changes || got.Pinned != c.pinned {
			t.Errorf("%s: changes %d pinned %v\n got: %q\nwant: %q", c.name, got.Changes, got.Pinned, got.Content, c.want)
		}
	}
}
