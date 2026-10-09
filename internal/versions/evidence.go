// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package versions

import (
	"context"
	"time"

	"github.com/pipozzz/goliash/internal/registry"
	"github.com/pipozzz/goliash/internal/store"
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
