// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: Apache-2.0

package registry

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// ImageSBOM returns the package URLs (purls) the SBOM of digest lists, from the first of: the SPDX or
// CycloneDX attestation BuildKit attaches to an image index (docker buildx build --sbom=true), a cosign
// attestation (cosign attest --type cyclonedx|spdxjson, the sha256-<hex>.att tag), or an OCI referrer: an
// SBOM pushed as an artifact (oras attach) or a Sigstore bundle with an SBOM predicate. It returns nil
// without error when the image has none.
func (c *Client) ImageSBOM(ctx context.Context, repository, digest string, creds Credentials) ([]string, error) {
	return c.imageSBOM(ctx, repository, digest, creds, true)
}

func (c *Client) imageSBOM(ctx context.Context, repository, digest string, creds Credentials, descend bool) ([]string, error) {
	host, repo, err := splitRepository(repository)
	if err != nil {
		return nil, err
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
	img, err := get(digest)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	add := func(purls []string) {
		for _, p := range purls {
			seen[p] = true
		}
	}
	blob := func(d string) ([]byte, error) {
		body, _, err := c.get(ctx, base+"/blobs/"+d, repo, creds, "*/*")
		return body, err
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
			p := l.Annotations[annotationPredicate]
			if !strings.Contains(p, "spdx") && !strings.Contains(p, "cyclonedx") {
				continue
			}
			body, err := blob(l.Digest)
			if err != nil {
				return nil, err
			}
			add(sbomPurls(body))
		}
		if len(seen) > 0 {
			break // one platform's SBOM stands for the image
		}
	}
	if len(seen) == 0 {
		// cosign attest: the .att tag's layers are DSSE envelopes around an in-toto statement.
		if att, err := get("sha256-" + strings.TrimPrefix(digest, "sha256:") + ".att"); err == nil {
			for _, l := range att.Layers {
				if !sbomPredicate(l.Annotations["predicateType"]) {
					continue
				}
				body, err := blob(l.Digest)
				if err != nil {
					return nil, err
				}
				add(sbomPurls(body))
			}
		}
	}
	if len(seen) == 0 {
		// OCI 1.1 referrers: an SBOM document pushed as an artifact, or a Sigstore bundle wrapping one.
		if body, _, err := c.get(ctx, base+"/referrers/"+digest, repo, creds, mediaIndex); err == nil {
			var idx evidenceManifest
			_ = json.Unmarshal(body, &idx)
			for _, d := range idx.Manifests {
				t := d.ArtifactType
				if !strings.Contains(t, mediaSPDX) && !strings.Contains(t, mediaCycloneDX) &&
					(!strings.HasPrefix(t, mediaSigstoreBundle) || !sbomPredicate(d.Annotations["dev.sigstore.bundle.predicateType"])) {
					continue
				}
				m, err := get(d.Digest)
				if err != nil {
					continue
				}
				for _, l := range m.Layers {
					if body, err := blob(l.Digest); err == nil {
						add(sbomPurls(body))
					}
				}
				if len(seen) > 0 {
					break
				}
			}
		}
	}
	// An index's SBOM may only list its platform images (Chainguard attests each platform apart): read the
	// SBOM of the linux/amd64 image, or the first one, instead.
	if onlyImages(seen) && descend {
		if child := platformImage(img.Manifests); child != "" {
			return c.imageSBOM(ctx, repository, child, creds, false)
		}
	}
	out := make([]string, 0, len(seen))
	for p := range seen {
		out = append(out, p)
	}
	sort.Strings(out)
	return out, nil
}

// onlyImages reports whether purls name nothing but OCI images.
func onlyImages(purls map[string]bool) bool {
	for p := range purls {
		if !strings.HasPrefix(p, "pkg:oci/") {
			return false
		}
	}
	return len(purls) > 0
}

// platformImage picks an index's linux/amd64 image, else its first image that is not an attestation.
func platformImage(ms []descriptor) string {
	first := ""
	for _, d := range ms {
		if d.Annotations[annotationRefType] == "attestation-manifest" || d.Platform == nil || d.Platform.OS == "unknown" {
			continue
		}
		if d.Platform.OS == "linux" && d.Platform.Architecture == "amd64" {
			return d.Digest
		}
		if first == "" {
			first = d.Digest
		}
	}
	return first
}

// sbomPredicate reports whether an in-toto predicate type names an SPDX or CycloneDX document.
func sbomPredicate(p string) bool {
	p = strings.ToLower(p)
	return strings.Contains(p, "spdx") || strings.Contains(p, "cyclonedx")
}

// sbomPurls reads the package URLs of an SBOM in any of the shapes registries hold it in: an in-toto
// statement with the document as its predicate, a DSSE envelope (cosign) or Sigstore bundle around such a
// statement, or the bare SPDX or CycloneDX JSON document.
func sbomPurls(body []byte) []string { return sbomPurlsIn(body, 0) }

func sbomPurlsIn(body []byte, depth int) []string {
	if depth > 3 {
		return nil
	}
	var v struct {
		Payload string `json:"payload"`
		DSSE    *struct {
			Payload string `json:"payload"`
		} `json:"dsseEnvelope"`
		Predicate json.RawMessage `json:"predicate"`
	}
	if json.Unmarshal(body, &v) != nil {
		return nil
	}
	payload := v.Payload
	if v.DSSE != nil {
		payload = v.DSSE.Payload
	}
	if payload != "" {
		inner, err := base64.StdEncoding.DecodeString(payload)
		if err != nil {
			return nil
		}
		return sbomPurlsIn(inner, depth+1)
	}
	if len(v.Predicate) > 0 && v.Predicate[0] == '{' {
		return documentPurls(v.Predicate)
	}
	return documentPurls(body)
}

// documentPurls reads the package URLs of an SPDX document (packages' purl external references) or a
// CycloneDX BOM (components' purl, nested ones included).
func documentPurls(doc []byte) []string {
	var st struct {
		Packages []struct {
			ExternalRefs []struct {
				ReferenceType    string `json:"referenceType"`
				ReferenceLocator string `json:"referenceLocator"`
			} `json:"externalRefs"`
		} `json:"packages"`
		Components []cycloneComponent `json:"components"`
	}
	if json.Unmarshal(doc, &st) != nil {
		return nil
	}
	var out []string
	for _, p := range st.Packages {
		for _, r := range p.ExternalRefs {
			if r.ReferenceType == "purl" && strings.HasPrefix(r.ReferenceLocator, "pkg:") {
				out = append(out, r.ReferenceLocator)
			}
		}
	}
	var walk func([]cycloneComponent)
	walk = func(cs []cycloneComponent) {
		for _, c := range cs {
			if strings.HasPrefix(c.Purl, "pkg:") {
				out = append(out, c.Purl)
			}
			walk(c.Components)
		}
	}
	walk(st.Components)
	return out
}

type cycloneComponent struct {
	Purl       string             `json:"purl"`
	Components []cycloneComponent `json:"components"`
}
