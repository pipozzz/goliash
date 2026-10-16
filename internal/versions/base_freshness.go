// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package versions

import (
	"context"

	"github.com/pipozzz/goliash/internal/registry"
	"github.com/pipozzz/goliash/internal/store"
)

// An image can be built today on a base from months ago: a build cache or a FROM without --pull keeps the old
// base's layers, and with them the old base's vulnerabilities, while the image looks new. Goliash compares the
// image's layers with the base's tag as it is now.

// BaseReader reads what a base image's tag points to now.
type BaseReader interface {
	CurrentBase(ctx context.Context, repository, tag string, creds registry.Credentials) (registry.Base, error)
}

// baseLookups remembers each base's current build for one run.
type baseLookups map[string]*registry.Base

// judgeBase records on ev whether an image with layers, of service svc, is built on its base as the base is now.
// It records nothing when the base is unknown, pinned to a digest (on purpose), or not readable.
func (c *Checker) judgeBase(ctx context.Context, ev *store.ImageEvidence, svc store.Service, layers []string, auth RegistryAuth, seen baseLookups) {
	declared := BaseImage(svc)
	reader, ok := c.tags.(BaseReader)
	if declared == "" || len(layers) == 0 || !ok {
		return
	}
	ref := ParseImage(declared)
	if ref.Digest != "" || !auth.Reads(ref.Repo()) {
		return
	}
	key := ref.Repo() + ":" + ref.Tag
	b, done := seen[key]
	if !done {
		creds, _ := auth.For(ref.Repo())
		if cur, err := reader.CurrentBase(ctx, ref.Repo(), ref.Tag, creds); err == nil && len(cur.Layers) > 0 {
			b = &cur
		}
		seen[key] = b
	}
	if b == nil {
		return
	}
	ev.BaseImage, ev.BaseStatus, ev.BaseSince = key, "outdated", b.Created
	if b.BuiltOn(layers) {
		ev.BaseStatus = "current"
	}
}
