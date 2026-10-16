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
	// Signers are who signed it, as the certificates of keyless signatures name them; empty for key-based
	// signatures and when no signature carries a certificate.
	Signers []Signer
	// Size is what pulling the image downloads: its compressed layers and config, in bytes, for linux/amd64
	// (or the first platform) of a multi-platform image; zero when unknown.
	Size int64
	// Layers are that image's layer digests, bottom first: the ones it shares with its base come first.
	Layers []string
	// LayerDetails are the same layers with their sizes and the build step that made each, from the image's
	// config history, when it could be read.
	LayerDetails []Layer
}

// Layer is one layer of an image: its digest, compressed size, and the Dockerfile step that made it.
type Layer struct {
	Digest  string `json:"digest"`
	Size    int64  `json:"size"`
	Command string `json:"command,omitempty"`
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
	Size         int64             `json:"size"`
	Annotations  map[string]string `json:"annotations"`
	Platform     *struct {
		OS           string `json:"os"`
		Architecture string `json:"architecture"`
	} `json:"platform"`
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
	readImage := func(m evidenceManifest) {
		ev.Size, ev.Layers = imageSize(m), layerDigests(m)
		ev.LayerDetails = c.layerDetails(ctx, base, repo, creds, m)
	}
	if len(img.Manifests) == 0 {
		readImage(img)
	} else if d, ok := platformManifest(img.Manifests); ok {
		if m, err := get(d.Digest); err == nil {
			readImage(m)
		}
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
		for _, l := range sig.Layers {
			if s, ok := certSigner([]byte(l.Annotations[annotationCosignCert])); ok {
				ev.addSigner(s)
			}
		}
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
					if m, err := get(d.Digest); err == nil && len(m.Layers) > 0 {
						if b, _, err := c.get(ctx, base+"/blobs/"+m.Layers[0].Digest, repo, creds, "*/*"); err == nil {
							if s, ok := bundleSigner(b); ok {
								ev.addSigner(s)
							}
						}
					}
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

// imageSize is what pulling an image manifest downloads: its config and layers, compressed.
func imageSize(m evidenceManifest) int64 {
	n := m.Config.Size
	for _, l := range m.Layers {
		n += l.Size
	}
	return n
}

// platformManifest picks the image of an index whose size is shown: linux/amd64, else the first real
// platform (not an attestation, whose platform is unknown/unknown).
func platformManifest(ds []descriptor) (descriptor, bool) {
	var first *descriptor
	for i, d := range ds {
		if d.Annotations[annotationRefType] == "attestation-manifest" || d.Platform == nil || d.Platform.OS == "unknown" {
			continue
		}
		if d.Platform.OS == "linux" && d.Platform.Architecture == "amd64" {
			return d, true
		}
		if first == nil {
			first = &ds[i]
		}
	}
	if first == nil {
		return descriptor{}, false
	}
	return *first, true
}

func layerDigests(m evidenceManifest) []string {
	out := make([]string, 0, len(m.Layers))
	for _, l := range m.Layers {
		out = append(out, l.Digest)
	}
	return out
}

// layerDetails pairs an image's layers with the build steps that made them, from its config's history (whose
// entries without a layer, like ENV or CMD, are skipped). Without a readable history the commands stay empty.
func (c *Client) layerDetails(ctx context.Context, base, repo string, creds Credentials, m evidenceManifest) []Layer {
	out := make([]Layer, len(m.Layers))
	for i, l := range m.Layers {
		out[i] = Layer{Digest: l.Digest, Size: l.Size}
	}
	if m.Config.Digest == "" {
		return out
	}
	body, _, err := c.get(ctx, base+"/blobs/"+m.Config.Digest, repo, creds, "*/*")
	if err != nil {
		return out
	}
	var cfg struct {
		History []struct {
			CreatedBy  string `json:"created_by"`
			EmptyLayer bool   `json:"empty_layer"`
		} `json:"history"`
	}
	if json.Unmarshal(body, &cfg) != nil {
		return out
	}
	i := 0
	for _, h := range cfg.History {
		if h.EmptyLayer {
			continue
		}
		if i >= len(out) {
			break
		}
		out[i].Command = buildStep(h.CreatedBy)
		i++
	}
	return out
}

// buildStep makes a history entry read like the Dockerfile line it came from, and short.
func buildStep(createdBy string) string {
	s := strings.TrimSpace(createdBy)
	for _, p := range []string{"/bin/sh -c #(nop) ", "|0 /bin/sh -c ", "/bin/sh -c "} {
		if i := strings.Index(s, p); i >= 0 && i < 12 {
			s = strings.TrimSpace(s[i+len(p):])
			if p != "/bin/sh -c #(nop) " && !strings.HasPrefix(s, "RUN ") {
				s = "RUN " + s
			}
		}
	}
	s = strings.TrimSuffix(s, " # buildkit")
	s = strings.ReplaceAll(s, "RUN /bin/sh -c ", "RUN ")
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > 160 {
		s = string(r[:159]) + "…"
	}
	return s
}
