// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package versions

import (
	"context"
	"time"

	"github.com/pipozzz/goliash/internal/registry"
	"github.com/pipozzz/goliash/internal/store"
	"github.com/pipozzz/goliash/pkg/agentproto"
)

// EvidenceReader finds the signatures and attestations of an image. registry.Client implements it.
type EvidenceReader interface {
	ImageEvidence(ctx context.Context, repository, digest string, creds registry.Credentials) (registry.Evidence, error)
}

// How long a lookup is trusted: digests do not change, but a signature or attestation can be added later.
const (
	evidenceFound    = 7 * 24 * time.Hour
	evidenceNotFound = 24 * time.Hour
	evidenceFailed   = 6 * time.Hour
	evidencePerRun   = 40 // lookups per workspace and run, to be gentle with registries
)

// CheckEvidence looks for signatures and attestations of the running images on public registries whose
// last lookup is out of date, at most evidencePerRun of them.
func (c *Checker) CheckEvidence(ctx context.Context, sc store.Scope) error {
	reader, ok := c.tags.(EvidenceReader)
	if !ok {
		return nil
	}
	active, err := c.store.ListActiveInstances(ctx, sc)
	if err != nil {
		return err
	}
	known, err := c.store.ListImageEvidence(ctx, sc)
	if err != nil {
		return err
	}
	now := c.now()
	done := map[string]bool{}
	n := 0
	for _, in := range active {
		if in.Digest == "" || n >= evidencePerRun {
			continue
		}
		repo := ParseImage(in.Image).Repo()
		key := repo + "@" + in.Digest
		if done[key] || !IsPublicRegistry(repo) || fresh(known[key], now) {
			continue
		}
		done[key] = true
		n++
		ev, err := reader.ImageEvidence(ctx, repo, in.Digest, registry.Credentials{})
		if ctx.Err() != nil {
			return ctx.Err()
		}
		rec := store.ImageEvidence{Repo: repo, Digest: in.Digest, Signed: ev.Signed, SBOM: ev.SBOM, Provenance: ev.Provenance, Found: ev.Found}
		if err != nil {
			rec.Error = err.Error()
		}
		if err := c.store.SetImageEvidence(ctx, sc, rec); err != nil {
			return err
		}
	}
	return nil
}

func fresh(ev store.ImageEvidence, now time.Time) bool {
	if ev.CheckedAt.IsZero() {
		return false
	}
	ttl := evidenceNotFound
	switch {
	case ev.Error != "":
		ttl = evidenceFailed
	case ev.Signed || ev.SBOM || ev.Provenance:
		ttl = evidenceFound
	}
	return now.Sub(ev.CheckedAt) < ttl
}

// inspectPerRepo bounds the digests an agent is asked to inspect per repository and round.
const inspectPerRepo = 20

// pendingInspections lists, per private repository, the running digests whose signatures, attestations and
// SBOM the agents should read: those without a fresh lookup.
func (c *Checker) pendingInspections(ctx context.Context, sc store.Scope, active []store.Instance) (map[string][]string, error) {
	known, err := c.store.ListImageEvidence(ctx, sc)
	if err != nil {
		return nil, err
	}
	now := c.now()
	out := map[string][]string{}
	seen := map[string]bool{}
	for _, in := range active {
		repo := ParseImage(in.Image).Repo()
		key := repo + "@" + in.Digest
		if in.Digest == "" || seen[key] || IsPublicRegistry(repo) || PackageRepo(repo) || fresh(known[key], now) ||
			len(out[repo]) >= inspectPerRepo || !validDigest(in.Digest) {
			continue
		}
		seen[key] = true
		out[repo] = append(out[repo], in.Digest)
	}
	return out, nil
}

func validDigest(d string) bool {
	if len(d) != len("sha256:")+64 || d[:7] != "sha256:" {
		return false
	}
	for _, r := range d[7:] {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
			return false
		}
	}
	return true
}

// RecordInspections records what an agent found beside private images: signatures and attestations, and the
// packages of their SBOMs.
func (c *Checker) RecordInspections(ctx context.Context, sc store.Scope, repo string, found []agentproto.ImageInspection) error {
	for _, in := range found {
		ev := store.ImageEvidence{Repo: repo, Digest: in.Digest, Found: in.Found}
		if in.Error != nil {
			ev.Error = *in.Error
		}
		ev.Signed = in.Signed != nil && *in.Signed
		ev.SBOM = in.Sbom != nil && *in.Sbom
		ev.Provenance = in.Provenance != nil && *in.Provenance
		if err := c.store.SetImageEvidence(ctx, sc, ev); err != nil {
			return err
		}
		if ev.SBOM && len(in.Purls) > 0 {
			if err := c.store.SetImageSBOM(ctx, sc, store.ImageSBOM{Repo: repo, Digest: in.Digest, Purls: in.Purls}); err != nil {
				return err
			}
		}
	}
	return nil
}
