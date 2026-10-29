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
	auth := c.registryAuth(ctx, sc)
	services, err := c.servicesByID(ctx, sc)
	if err != nil {
		return err
	}
	bases := newBaseLookups()
	done := map[string]bool{}
	n := 0
	for _, in := range active {
		if in.Digest == "" || n >= evidencePerRun {
			continue
		}
		repo := ParseImage(in.Image).Repo()
		key := repo + "@" + in.Digest
		if done[key] || !auth.Reads(repo) || fresh(known[key], now) {
			continue
		}
		done[key] = true
		n++
		creds, _ := auth.For(repo)
		ev, err := reader.ImageEvidence(ctx, repo, in.Digest, creds)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		rec := store.ImageEvidence{Repo: repo, Digest: in.Digest, Signed: ev.Signed, SBOM: ev.SBOM, Provenance: ev.Provenance, Found: ev.Found, Size: ev.Size, Components: ev.Components}
		for _, s := range ev.Signers {
			rec.Signers = append(rec.Signers, store.ImageSigner(s))
		}
		for _, l := range ev.LayerDetails {
			rec.Layers = append(rec.Layers, store.ImageLayer{Digest: l.Digest, Size: l.Size, Command: l.Command})
		}
		if err != nil {
			rec.Error = err.Error()
		} else {
			c.judgeBase(ctx, &rec, services[in.ServiceID], ev.Layers, auth, bases)
		}
		if err := c.store.SetImageEvidence(ctx, sc, rec); err != nil {
			return err
		}
	}
	return nil
}

func (c *Checker) servicesByID(ctx context.Context, sc store.Scope) (map[string]store.Service, error) {
	svcs, err := c.store.ListServices(ctx, sc)
	if err != nil {
		return nil, err
	}
	out := make(map[string]store.Service, len(svcs))
	for _, s := range svcs {
		out[s.ID] = s
	}
	return out, nil
}

// sizesRead is when lookups started reading image sizes (Goliash 1.61).
var sizesRead = time.Date(2026, 10, 16, 0, 0, 0, 0, time.UTC)

func fresh(ev store.ImageEvidence, now time.Time) bool {
	// Looked up before sizes were read: once more, to learn the size.
	if ev.CheckedAt.IsZero() || (ev.Size == 0 && ev.Error == "" && ev.CheckedAt.Before(sizesRead)) {
		return false
	}
	// Signed, and looked up before signers were read: once more, to learn who signed it.
	if ev.Signed && ev.Error == "" && !ev.SignersRead {
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
	sboms, err := c.store.ListImageSBOMs(ctx, sc)
	if err != nil {
		return nil, err
	}
	now := c.now()
	auth := c.registryAuth(ctx, sc)
	out := map[string][]string{}
	seen := map[string]bool{}
	for _, in := range active {
		repo := ParseImage(in.Image).Repo()
		key := repo + "@" + in.Digest
		if in.Digest == "" || seen[key] || auth.Reads(repo) || PackageRepo(repo) || (fresh(known[key], now) && !NeedsLicenses(sboms[key])) ||
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
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}

// RecordInspections records what an agent found beside private images: signatures and attestations, and the
// packages of their SBOMs.
func (c *Checker) RecordInspections(ctx context.Context, sc store.Scope, repo string, found []agentproto.ImageInspection) error {
	services, err := c.servicesByID(ctx, sc)
	if err != nil {
		return err
	}
	active, err := c.store.ListActiveInstances(ctx, sc)
	if err != nil {
		return err
	}
	serviceOf := map[string]string{} // digest -> service, for this repository
	for _, in := range active {
		if in.ServiceID != "" && ParseImage(in.Image).Repo() == repo {
			serviceOf[in.Digest] = in.ServiceID
		}
	}
	auth, bases := c.registryAuth(ctx, sc), newBaseLookups()
	for _, in := range found {
		ev := store.ImageEvidence{Repo: repo, Digest: in.Digest, Found: in.Found}
		if in.Error != nil {
			ev.Error = *in.Error
		}
		ev.Signed = in.Signed != nil && *in.Signed
		ev.SBOM = in.Sbom != nil && *in.Sbom
		ev.Provenance = in.Provenance != nil && *in.Provenance
		for _, s := range in.Signers {
			signer := store.ImageSigner{Identity: s.Identity}
			if s.Issuer != nil {
				signer.Issuer = *s.Issuer
			}
			ev.Signers = append(ev.Signers, signer)
		}
		if in.Size != nil {
			ev.Size = *in.Size
		}
		if in.Components != nil {
			ev.Components = *in.Components
		}
		for _, l := range in.LayerDetails {
			layer := store.ImageLayer{Digest: l.Digest, Size: l.Size}
			if l.Command != nil {
				layer.Command = *l.Command
			}
			ev.Layers = append(ev.Layers, layer)
		}
		if in.Error == nil {
			c.judgeBase(ctx, &ev, services[serviceOf[in.Digest]], in.Layers, auth, bases)
		}
		if err := c.store.SetImageEvidence(ctx, sc, ev); err != nil {
			return err
		}
		if in.Vex != nil {
			rec := store.ImageVEX{Repo: repo, Digest: in.Digest, Statements: make([]store.VEXStatement, 0, len(in.Vex))}
			for _, v := range in.Vex {
				rec.Statements = append(rec.Statements, store.VEXStatement{
					Vulnerability: v.Vulnerability, Status: v.Status, Justification: deref(v.Justification), Impact: deref(v.Impact),
					Packages: v.Packages, Author: deref(v.Author),
				})
			}
			if err := c.store.SetImageVEX(ctx, sc, rec); err != nil {
				return err
			}
		}
		fromPackages := in.PurlsFrom != nil && *in.PurlsFrom == agentproto.Packages
		if (ev.SBOM || fromPackages) && len(in.Purls) > 0 {
			src := "sbom"
			if fromPackages && !ev.SBOM {
				src = "packages"
			}
			if err := c.store.SetImageSBOM(ctx, sc, store.ImageSBOM{Repo: repo, Digest: in.Digest, Purls: in.Purls, Licenses: in.Licenses, Source: src}); err != nil {
				return err
			}
		}
	}
	return nil
}

// deref is an optional field's value, "" when absent.
func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
