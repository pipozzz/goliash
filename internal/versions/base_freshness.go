// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package versions

import (
	"context"
	"strings"

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

// baseLookups remembers each base's current build, and the chain below it, for one run.
type baseLookups struct {
	builds map[string]*registry.Base
	chains map[string][]store.BaseLink
}

func newBaseLookups() baseLookups {
	return baseLookups{builds: map[string]*registry.Base{}, chains: map[string][]store.BaseLink{}}
}

// build reads what repo:ref is now, once a run: nil when it cannot be read or has no layers.
func (s baseLookups) build(ctx context.Context, reader BaseReader, repo, ref string, creds registry.Credentials) *registry.Base {
	key := repo + ":" + ref
	if b, done := s.builds[key]; done {
		return b
	}
	var b *registry.Base
	if cur, err := reader.CurrentBase(ctx, repo, ref, creds); err == nil && len(cur.Layers) > 0 {
		b = &cur
	}
	s.builds[key] = b
	return b
}

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
	creds, _ := auth.For(ref.Repo())
	b := seen.build(ctx, reader, ref.Repo(), ref.Tag, creds)
	if b == nil {
		return
	}
	ev.BaseImage, ev.BaseStatus, ev.BaseSince = key, "outdated", b.Created
	if b.BuiltOn(layers) {
		ev.BaseStatus = "current"
	}
	// Down the chain: the base's own base, and so on. An outdated link below makes the image's base outdated too:
	// its fixes have not reached the image, though the image is built on its base as that is now.
	if below := c.baseChain(ctx, key, b.Layers, auth, seen, 0); len(below) > 0 {
		first := store.BaseLink{Image: ev.Repo, Base: key, Status: ev.BaseStatus, Since: b.Created}
		ev.BaseChain = append([]store.BaseLink{first}, below...)
		if l, ok := StaleLink(ev.BaseChain); ok {
			ev.BaseStatus, ev.BaseSince = "outdated", l.Since
		}
	}
}

// chainDepth bounds how far down a chain of bases Goliash follows.
const chainDepth = 4

// BuildBaseReader reads which images an image was built from, from its BuildKit provenance.
type BuildBaseReader interface {
	BuildBases(ctx context.Context, repository, reference string, creds registry.Credentials) ([]string, error)
}

// baseChain follows image (repository:tag, whose current build has layers) down its bases: each link says what
// an image is built on and whether on its current build. It stops at an official Docker image (their maintainers
// rebuild them on their bases), at an image whose base it cannot tell (no base label, no provenance) or read, and
// after chainDepth links.
func (c *Checker) baseChain(ctx context.Context, image string, layers []string, auth RegistryAuth, seen baseLookups, depth int) []store.BaseLink {
	if depth >= chainDepth || len(layers) == 0 || strings.HasPrefix(image, "docker.io/library/") {
		return nil
	}
	if links, ok := seen.chains[image]; ok {
		return links
	}
	seen.chains[image] = nil // a chain that loops back ends here
	link, layersBelow, ok := c.baseOf(ctx, image, layers, auth, seen)
	if !ok {
		return nil
	}
	links := append([]store.BaseLink{link}, c.baseChain(ctx, link.Base, layersBelow, auth, seen, depth+1)...)
	seen.chains[image] = links
	return links
}

// baseOf finds what image (repository:tag, whose current build has layers) is built on: the base its label names,
// else the image of its provenance whose layers it starts with. It returns the link and the base's current layers.
func (c *Checker) baseOf(ctx context.Context, image string, layers []string, auth RegistryAuth, seen baseLookups) (store.BaseLink, []string, bool) {
	reader, ok := c.tags.(BaseReader)
	ref := ParseImage(image)
	if !ok || !auth.Reads(ref.Repo()) {
		return store.BaseLink{}, nil, false
	}
	creds, _ := auth.For(ref.Repo())
	var candidates []string
	how := "label"
	if mr, ok := c.tags.(SourceReader); ok {
		if meta, err := mr.ImageMeta(ctx, ref.Repo(), ref.Tag, creds); err == nil && meta.Base != "" {
			candidates = []string{meta.Base}
		}
	}
	if len(candidates) == 0 {
		if pr, ok := c.tags.(BuildBaseReader); ok {
			candidates, _ = pr.BuildBases(ctx, ref.Repo(), ref.Tag, creds)
			how = "provenance"
		}
	}
	for _, cand := range candidates {
		cref := ParseImage(cand)
		if cref.Tag == "" || !auth.Reads(cref.Repo()) {
			continue
		}
		ccreds, _ := auth.For(cref.Repo())
		cur := seen.build(ctx, reader, cref.Repo(), cref.Tag, ccreds)
		if cur == nil {
			continue
		}
		link := store.BaseLink{Image: image, Base: cref.Repo() + ":" + cref.Tag, Status: "outdated", Since: cur.Created, How: how}
		switch {
		case cur.BuiltOn(layers):
			link.Status = "current"
		case cref.Digest != "":
			// A provenance names every stage's images: this is the base if the image starts with its layers as
			// they were when it was built.
			then := seen.build(ctx, reader, cref.Repo(), cref.Digest, ccreds)
			if then == nil || !then.BuiltOn(layers) {
				continue
			}
		case how != "label":
			continue
		}
		return link, cur.Layers, true
	}
	return store.BaseLink{}, nil, false
}

// OutdatedBase says what an image with an outdated base is behind on: the base whose newer build it lacks, and the
// image of the chain to rebuild first ("" when it is the image itself): an image built on registry/b:1 as it is
// now, which is built on an older alpine:3.20, lacks alpine's build until b is rebuilt.
func OutdatedBase(ev store.ImageEvidence) (stale, rebuild string) {
	if l, ok := StaleLink(ev.BaseChain); ok && l.Image != ev.Repo {
		return l.Base, l.Image
	}
	return ev.BaseImage, ""
}

// StaleLink is the first link of a chain built on an older build of its base: the image to rebuild first.
func StaleLink(chain []store.BaseLink) (store.BaseLink, bool) {
	for _, l := range chain {
		if l.Status == "outdated" {
			return l, true
		}
	}
	return store.BaseLink{}, false
}

// ShortImage is an image as people write it: node:22-alpine, ghcr.io/acme/base:1.
func ShortImage(image string) string {
	return strings.TrimPrefix(strings.TrimPrefix(image, "docker.io/library/"), "docker.io/")
}

// chainVia names the images between an image's base and the one to rebuild: "whose ghcr.io/acme/base:1 is " or
// "which is " when the base itself is the one.
func chainVia(ev store.ImageEvidence, rebuild string) string {
	if rebuild == ev.BaseImage {
		return "which is "
	}
	return "whose " + ShortImage(rebuild) + " is "
}

// ChainText draws a chain of bases: "ghcr.io/acme/base:1 (current) → alpine:3.20 (outdated)".
func ChainText(chain []store.BaseLink) string {
	parts := make([]string, len(chain))
	for i, l := range chain {
		parts[i] = ShortImage(l.Base) + " (" + l.Status + ")"
	}
	return strings.Join(parts, " → ")
}
