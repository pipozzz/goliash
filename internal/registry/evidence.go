// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: Apache-2.0

package registry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// Evidence is what a registry holds about one image beside the image itself: signatures and attestations.
// It records that they exist, not that they verify.
type Evidence struct {
	Signed     bool     // a cosign or Notation signature, or a Sigstore bundle
	SBOM       bool     // an SPDX or CycloneDX SBOM attestation or artifact
	Provenance bool     // a SLSA provenance attestation
	Found      []string // what was found, for people: "cosign signature", "buildkit sbom" …
}

const (
	mediaSigstoreBundle = "application/vnd.dev.sigstore.bundle"
	mediaNotation       = "application/vnd.cncf.notary.signature"
	mediaCosignSig      = "application/vnd.dev.cosign.artifact.sig"
	mediaSPDX           = "spdx"
	mediaCycloneDX      = "cyclonedx"
	annotationRefType   = "vnd.docker.reference.type"
	annotationPredicate = "in-toto.io/predicate-type"
)

type descriptor struct {
	MediaType    string            `json:"mediaType"`
	ArtifactType string            `json:"artifactType"`
	Digest       string            `json:"digest"`
	Annotations  map[string]string `json:"annotations"`
}

type evidenceManifest struct {
	MediaType    string            `json:"mediaType"`
	ArtifactType string            `json:"artifactType"`
	Annotations  map[string]string `json:"annotations"`
	Manifests    []descriptor      `json:"manifests"`
	Layers       []descriptor      `json:"layers"`
	Config       descriptor        `json:"config"`
}

// ImageEvidence looks for the signatures and attestations of digest in repository: attestation manifests
// BuildKit adds to an image index, cosign's sha256-<hex>.sig and .att tags, and OCI referrers.
func (c *Client) ImageEvidence(ctx context.Context, repository, digest string, creds Credentials) (Evidence, error) {
	var ev Evidence
	host, repo, err := splitRepository(repository)
	if err != nil {
		return ev, err
	}
	if !strings.HasPrefix(digest, "sha256:") {
		return ev, fmt.Errorf("not a sha256 digest: %q", digest)
	}
	base := fmt.Sprintf("%s://%s/v2/%s", c.Scheme, host, repo)
	accept := strings.Join([]string{mediaIndex, mediaDockerList, mediaManifest, mediaDockerImage}, ", ")
	get := func(ref string) (evidenceManifest, error) {
		var m evidenceManifest
		body, _, err := c.get(ctx, base+"/manifests/"+ref, repo, creds, accept)
		if err != nil {
			return m, err
		}
		return m, json.Unmarshal(body, &m)
	}
	found := func(what string) {
		for _, f := range ev.Found {
			if f == what {
				return
			}
		}
		ev.Found = append(ev.Found, what)
	}
	predicate := func(p, how string) {
		switch {
		case strings.Contains(p, "spdx") || strings.Contains(p, "cyclonedx"):
			ev.SBOM = true
			found(how + " sbom")
		case strings.Contains(p, "slsa.dev/provenance"):
			ev.Provenance = true
			found(how + " provenance")
		}
	}

	// The image: BuildKit keeps attestations as manifests of the index, marked attestation-manifest.
	img, err := get(digest)
	if err != nil {
		return ev, err
	}
	for _, d := range img.Manifests {
		if d.Annotations[annotationRefType] != "attestation-manifest" {
			continue
		}
		att, err := get(d.Digest)
		if err != nil {
			continue
		}
		for _, l := range att.Layers {
			predicate(l.Annotations[annotationPredicate], "buildkit")
		}
	}

	// cosign's tags: sha256-<hex>.sig for signatures, .att for attestations (predicate in each layer).
	tag := "sha256-" + strings.TrimPrefix(digest, "sha256:")
	if sig, err := get(tag + ".sig"); err == nil && len(sig.Layers) > 0 {
		ev.Signed = true
		found("cosign signature")
	} else if err != nil && !errors.Is(err, ErrNotFound) {
		return ev, err
	}
	if att, err := get(tag + ".att"); err == nil {
		for _, l := range att.Layers {
			if p := l.Annotations["predicateType"]; p != "" {
				predicate(p, "cosign")
			}
		}
	}

	// OCI 1.1 referrers: Sigstore bundles, Notation signatures, SBOMs and attestations pushed as artifacts.
	body, _, err := c.get(ctx, base+"/referrers/"+digest, repo, creds, "application/vnd.oci.image.index.v1+json")
	if err == nil {
		var idx evidenceManifest
		if json.Unmarshal(body, &idx) == nil {
			for _, d := range idx.Manifests {
				t := d.ArtifactType
				switch {
				case strings.HasPrefix(t, mediaSigstoreBundle):
					ev.Signed = true
					found("sigstore bundle")
					predicate(d.Annotations["dev.sigstore.bundle.predicateType"], "sigstore")
				case t == mediaNotation:
					ev.Signed = true
					found("notation signature")
				case t == mediaCosignSig:
					ev.Signed = true
					found("cosign signature")
				case strings.Contains(t, mediaSPDX) || strings.Contains(t, mediaCycloneDX):
					ev.SBOM = true
					found("sbom artifact")
				default:
					predicate(d.Annotations[annotationPredicate], "referrer")
				}
			}
		}
	}
	return ev, nil
}
