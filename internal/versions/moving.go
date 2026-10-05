// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package versions

import (
	"context"
	"slices"
	"sort"
	"time"

	"github.com/pipozzz/goliash/internal/registry"
	"github.com/pipozzz/goliash/internal/store"
)

// A moving tag ("1", "18-alpine", "latest") points at whatever release its
// publisher last pushed under it. The digest an image runs with says which one: the
// server compares it with the digests of the tags it could be and remembers the
// match, so the matrix shows "1 = 1.27.3" and drift compares the exact version.

// DigestReader reads the digests a tag stands for. registry.Client implements it.
type DigestReader interface {
	// TagDigest is the digest of the tag's manifest or index, read with HEAD (Docker
	// Hub does not count it as a pull).
	TagDigest(ctx context.Context, repository, reference string, creds registry.Credentials) (string, error)
	// ManifestDigests adds the digest of each platform's manifest, read with GET.
	ManifestDigests(ctx context.Context, repository, reference string, creds registry.Credentials) ([]string, error)
}

const (
	// movingLookups bounds the tags tried per running digest with HEAD, and
	// movingPlatformLookups those whose platform manifests are read when the running
	// digest is a platform's rather than the index's.
	movingLookups         = 8
	movingPlatformLookups = 2
	// movingRetry is how long a digest no tag matched waits before it is tried again.
	movingRetry = 24 * time.Hour
)

// IsMoving reports whether tag names a line of releases rather than one release.
func IsMoving(tag string) bool {
	v, ok := ParseVersion(tag)
	return !ok || (len(v.Parts) < 3 && v.Pre == "")
}

// movingCandidates lists the tags tag may stand for, newest and most specific first:
// longer versions with the same leading numbers and flavour ("1" -> 1.27.3, 1.27;
// "18-alpine" -> 18.0-alpine), or for a tag that is no version ("latest") the newest
// plain releases.
func movingCandidates(tag string, tags []string, limit int) []string {
	base, isVersion := ParseVersion(tag)
	var vs []Version
	for _, t := range tags {
		v, ok := ParseVersion(t)
		if !ok || v.Pre != "" || t == tag {
			continue
		}
		if isVersion {
			if v.Variant != base.Variant || len(v.Parts) <= len(base.Parts) || !slices.Equal(v.Parts[:len(base.Parts)], base.Parts) {
				continue
			}
		} else if v.Variant != "" {
			continue
		}
		vs = append(vs, v)
	}
	sort.SliceStable(vs, func(i, j int) bool {
		if c := vs[i].Compare(vs[j]); c != 0 {
			return c > 0
		}
		return len(vs[i].Parts) > len(vs[j].Parts)
	})
	out := make([]string, 0, min(limit, len(vs)))
	for _, v := range vs {
		if len(out) == limit {
			break
		}
		out = append(out, v.Raw)
	}
	return out
}

// resolveMoving finds the exact versions behind the moving tags service svcID runs
// from repo, whose tags are known, and records them. Each digest is looked up once
// (again after movingRetry when nothing matched). It reports whether it found a
// version not known before.
func (c *Checker) resolveMoving(ctx context.Context, sc store.Scope, st workspaceState, svcID, repo string, tags []string) (found bool) {
	reader, ok := c.tags.(DigestReader)
	if !ok {
		return false
	}
	known, err := c.store.TagResolutions(ctx, sc)
	if err != nil {
		c.log.WarnContext(ctx, "tag resolutions", "err", err)
		return false
	}
	done := map[string]bool{}
	for _, r := range known {
		if r.Repo == repo && (r.Version != "" || c.now().Sub(r.CheckedAt) < movingRetry) {
			done[r.Digest] = true
		}
	}
	for _, i := range st.instances {
		if !i.IsMain || i.ServiceID != svcID || i.Digest == "" || done[i.Digest] || !IsMoving(i.Tag) ||
			ParseImage(i.Image).Repo() != repo {
			continue
		}
		done[i.Digest] = true
		version := ""
		cands := movingCandidates(i.Tag, tags, movingLookups)
		for _, cand := range cands {
			d, err := reader.TagDigest(ctx, repo, cand, registry.Credentials{})
			if ctx.Err() != nil {
				return found
			}
			if err == nil && d == i.Digest {
				version = cand
				break
			}
		}
		for _, cand := range cands[:min(len(cands), movingPlatformLookups)] {
			if version != "" {
				break
			}
			digests, err := reader.ManifestDigests(ctx, repo, cand, registry.Credentials{})
			if ctx.Err() != nil {
				return found
			}
			if err == nil && slices.Contains(digests, i.Digest) {
				version = cand
			}
		}
		if err := c.store.SetTagResolution(ctx, sc, repo, i.Digest, version); err != nil {
			c.log.WarnContext(ctx, "record tag resolution", "repo", repo, "err", err)
			return found
		}
		if version != "" {
			found = true
			c.log.InfoContext(ctx, "moving tag resolved", "repo", repo, "tag", i.Tag, "version", version)
		}
	}
	return found
}

// resolvedDigests maps image digests to the exact versions found for them.
func resolvedDigests(ctx context.Context, st *store.Store, sc store.Scope) (map[string]string, error) {
	rs, err := st.TagResolutions(ctx, sc)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, r := range rs {
		if r.Version != "" {
			out[r.Digest] = r.Version
		}
	}
	return out, nil
}

// applyResolutions fills in the exact versions behind moving tags in the matrix.
func applyResolutions(m *Matrix, byDigest map[string]string) {
	fill := func(cells []Cell) {
		for ci := range cells {
			for vi := range cells[ci].Versions {
				v := &cells[ci].Versions[vi]
				if r, ok := byDigest[v.Digest]; ok && v.Digest != "" && IsMoving(v.Tag) {
					v.Resolved = r
				}
			}
		}
	}
	for ri := range m.Rows {
		fill(m.Rows[ri].Cells)
		for pi := range m.Rows[ri].Parts {
			fill(m.Rows[ri].Parts[pi].Cells)
		}
	}
}
