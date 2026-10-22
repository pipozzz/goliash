// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: Apache-2.0

package registry

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
)

// BuildBases lists the images an image was built from, as its BuildKit provenance attestation records them
// (SLSA v0.2 materials or v1 resolved dependencies): "docker.io/library/alpine:3.20@sha256:…". Every stage's
// images are there, builders too; which one the image's layers start with is for the caller to tell. It returns
// nothing for an image without such an attestation.
func (c *Client) BuildBases(ctx context.Context, repository, reference string, creds Credentials) ([]string, error) {
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
	img, err := get(reference)
	if err != nil {
		return nil, err
	}
	var out []string
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
			if !strings.Contains(l.Annotations[annotationPredicate], "slsa.dev/provenance") || l.Size > maxProvenance {
				continue
			}
			body, _, err := c.get(ctx, base+"/blobs/"+l.Digest, repo, creds, "*/*")
			if err != nil {
				continue
			}
			for _, b := range provenanceImages(body) {
				if !seen[b] {
					seen[b] = true
					out = append(out, b)
				}
			}
		}
		break // one platform's provenance names the bases of all
	}
	return out, nil
}

// maxProvenance bounds the provenance statement read: BuildKit's are a few kilobytes, mode=max ones more.
const maxProvenance = 4 << 20

// provenanceImages reads the container images an in-toto provenance statement names as build inputs.
func provenanceImages(body []byte) []string {
	var st struct {
		Predicate struct {
			Materials []struct {
				URI    string            `json:"uri"`
				Digest map[string]string `json:"digest"`
			} `json:"materials"`
			BuildDefinition struct {
				ResolvedDependencies []struct {
					URI    string            `json:"uri"`
					Digest map[string]string `json:"digest"`
				} `json:"resolvedDependencies"`
			} `json:"buildDefinition"`
		} `json:"predicate"`
	}
	if json.Unmarshal(body, &st) != nil {
		return nil
	}
	var out []string
	add := func(uri string, digest map[string]string) {
		if img := dockerPurl(uri, digest["sha256"]); img != "" {
			out = append(out, img)
		}
	}
	for _, m := range st.Predicate.Materials {
		add(m.URI, m.Digest)
	}
	for _, m := range st.Predicate.BuildDefinition.ResolvedDependencies {
		add(m.URI, m.Digest)
	}
	return out
}

// dockerPurl turns a docker package URL into an image reference: "pkg:docker/alpine@3.20?platform=linux%2Famd64"
// with its digest is "alpine:3.20@sha256:…". Other inputs (git, http) are "".
func dockerPurl(uri, sha string) string {
	rest, ok := strings.CutPrefix(uri, "pkg:docker/")
	if !ok {
		return ""
	}
	rest, _, _ = strings.Cut(rest, "?")
	rest, _ = url.PathUnescape(rest)
	name, version, _ := strings.Cut(rest, "@")
	if name == "" {
		return ""
	}
	ref := name
	if version != "" && !strings.HasPrefix(version, "sha256:") {
		ref += ":" + version
	}
	if sha != "" {
		ref += "@sha256:" + sha
	}
	return ref
}
