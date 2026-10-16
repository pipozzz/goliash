// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: Apache-2.0

package registry

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"
)

// Base is an image as its tag points now, to compare the images built on it with: are they built on this one?
type Base struct {
	Digest  string    // the tag's manifest or index digest
	Layers  []string  // layer digests, bottom first, for linux/amd64 (or the first platform)
	Created time.Time // when this image was built, from its config; zero when the config does not say
}

// CurrentBase reads what tag points to now in repository: its layers and when it was built.
func (c *Client) CurrentBase(ctx context.Context, repository, tag string, creds Credentials) (Base, error) {
	var b Base
	host, repo, err := splitRepository(repository)
	if err != nil {
		return b, err
	}
	base := fmt.Sprintf("%s://%s/v2/%s", c.Scheme, host, repo)
	accept := strings.Join([]string{mediaIndex, mediaDockerList, mediaManifest, mediaDockerImage}, ", ")
	get := func(ref string) (evidenceManifest, string, error) {
		var m evidenceManifest
		body, _, err := c.get(ctx, base+"/manifests/"+ref, repo, creds, accept)
		if err != nil {
			return m, "", err
		}
		sum := sha256.Sum256(body) // a manifest's digest is the hash of its bytes
		return m, "sha256:" + hex.EncodeToString(sum[:]), json.Unmarshal(body, &m)
	}
	m, digest, err := get(tag)
	if err != nil {
		return b, err
	}
	b.Digest = digest
	if len(m.Manifests) > 0 {
		d, ok := platformManifest(m.Manifests)
		if !ok {
			return b, fmt.Errorf("%s:%s has no image for a known platform", repository, tag)
		}
		if m, _, err = get(d.Digest); err != nil {
			return b, err
		}
	}
	b.Layers = layerDigests(m)
	if m.Config.Digest != "" {
		if body, _, err := c.get(ctx, base+"/blobs/"+m.Config.Digest, repo, creds, "*/*"); err == nil {
			var cfg struct {
				Created time.Time `json:"created"`
			}
			if json.Unmarshal(body, &cfg) == nil {
				b.Created = cfg.Created.UTC()
			}
		}
	}
	return b, nil
}

// BuiltOn reports whether an image with layers is built on base as it is now: base's layers come first, as a
// Dockerfile's FROM copies them. False means an older build of the base (a cached one, FROM without --pull),
// or another base than declared.
func (b Base) BuiltOn(layers []string) bool {
	return len(b.Layers) > 0 && len(layers) >= len(b.Layers) && slices.Equal(layers[:len(b.Layers)], b.Layers)
}
