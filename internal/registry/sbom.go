// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: Apache-2.0

package registry

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// ImageSBOM returns the package URLs (purls) the SBOM attestation of digest lists: the SPDX or CycloneDX
// attestation BuildKit attaches to an image index (docker buildx build --sbom=true). It returns nil
// without error when the image has none.
func (c *Client) ImageSBOM(ctx context.Context, repository, digest string, creds Credentials) ([]string, error) {
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
			body, _, err := c.get(ctx, base+"/blobs/"+l.Digest, repo, creds, "*/*")
			if err != nil {
				return nil, err
			}
			for _, purl := range purlsOf(body) {
				seen[purl] = true
			}
		}
		if len(seen) > 0 {
			break // one platform's SBOM stands for the image
		}
	}
	out := make([]string, 0, len(seen))
	for p := range seen {
		out = append(out, p)
	}
	sort.Strings(out)
	return out, nil
}

// purlsOf reads the package URLs of an in-toto statement whose predicate is an SPDX document (packages'
// purl external references) or a CycloneDX BOM (components' purl).
func purlsOf(statement []byte) []string {
	var st struct {
		Predicate struct {
			Packages []struct {
				ExternalRefs []struct {
					ReferenceType    string `json:"referenceType"`
					ReferenceLocator string `json:"referenceLocator"`
				} `json:"externalRefs"`
			} `json:"packages"`
			Components []cycloneComponent `json:"components"`
		} `json:"predicate"`
	}
	if json.Unmarshal(statement, &st) != nil {
		return nil
	}
	var out []string
	for _, p := range st.Predicate.Packages {
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
	walk(st.Predicate.Components)
	return out
}

type cycloneComponent struct {
	Purl       string             `json:"purl"`
	Components []cycloneComponent `json:"components"`
}
