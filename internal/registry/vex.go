// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: Apache-2.0

package registry

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
)

// An image's publisher can say which vulnerabilities its packages match but do not affect it: an OpenVEX document,
// attached as an attestation (docker buildx --attest type=vex, cosign attest --type openvex) or a referrer.

// VEXStatement is what a VEX document says of one vulnerability in the image.
type VEXStatement struct {
	Vulnerability string   `json:"vulnerability"`           // CVE-2023-44487, or another ID
	Status        string   `json:"status"`                  // not_affected, fixed, affected or under_investigation
	Justification string   `json:"justification,omitempty"` // vulnerable_code_not_present, …
	Impact        string   `json:"impact,omitempty"`        // the impact statement, why it does not affect it
	Packages      []string `json:"packages,omitempty"`      // the subcomponents it is about (purls); empty for all
	Author        string   `json:"author,omitempty"`
}

// vexPredicate reports whether an attestation's predicate type, or an artifact's type, is OpenVEX.
func vexPredicate(p string) bool { return strings.Contains(strings.ToLower(p), "openvex") }

// ImageVEX reads the OpenVEX statements attached to digest: BuildKit attestations, cosign's .att tag and OCI
// referrers. Later statements about the same vulnerability override earlier ones.
func (c *Client) ImageVEX(ctx context.Context, repository, digest string, creds Credentials) ([]VEXStatement, error) {
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
	var docs [][]byte
	blob := func(d string) {
		if body, _, err := c.get(ctx, base+"/blobs/"+d, repo, creds, "*/*"); err == nil {
			docs = append(docs, body)
		}
	}
	img, err := get(digest)
	if err != nil {
		return nil, err
	}
	for _, d := range img.Manifests {
		if d.Annotations[annotationRefType] != "attestation-manifest" {
			continue
		}
		if att, err := get(d.Digest); err == nil {
			for _, l := range att.Layers {
				if vexPredicate(l.Annotations[annotationPredicate]) {
					blob(l.Digest)
				}
			}
		}
	}
	if att, err := get("sha256-" + strings.TrimPrefix(digest, "sha256:") + ".att"); err == nil {
		for _, l := range att.Layers {
			if vexPredicate(l.Annotations["predicateType"]) {
				blob(l.Digest)
			}
		}
	}
	if body, _, err := c.get(ctx, base+"/referrers/"+digest, repo, creds, mediaIndex); err == nil {
		var idx evidenceManifest
		_ = json.Unmarshal(body, &idx)
		for _, d := range idx.Manifests {
			if !vexPredicate(d.ArtifactType) && !vexPredicate(d.Annotations[annotationPredicate]) &&
				!vexPredicate(d.Annotations["dev.sigstore.bundle.predicateType"]) {
				continue
			}
			if m, err := get(d.Digest); err == nil {
				for _, l := range m.Layers {
					blob(l.Digest)
				}
			}
		}
	}
	var out []VEXStatement
	for _, d := range docs {
		out = mergeVEX(out, ParseVEX(d))
	}
	return out, nil
}

// mergeVEX adds statements, a later one about the same vulnerability and packages taking the place of an earlier.
func mergeVEX(list, more []VEXStatement) []VEXStatement {
	for _, s := range more {
		replaced := false
		for i, x := range list {
			if x.Vulnerability == s.Vulnerability && strings.Join(x.Packages, " ") == strings.Join(s.Packages, " ") {
				list[i], replaced = s, true
			}
		}
		if !replaced {
			list = append(list, s)
		}
	}
	return list
}

// ParseVEX reads an OpenVEX document: bare, as an in-toto statement's predicate, or in a DSSE envelope or Sigstore
// bundle around one.
func ParseVEX(body []byte) []VEXStatement { return parseVEX(body, 0) }

func parseVEX(body []byte, depth int) []VEXStatement {
	if depth > 3 {
		return nil
	}
	var v struct {
		Payload string `json:"payload"`
		DSSE    *struct {
			Payload string `json:"payload"`
		} `json:"dsseEnvelope"`
		Predicate  json.RawMessage `json:"predicate"`
		Author     string          `json:"author"`
		Statements []struct {
			Vulnerability json.RawMessage `json:"vulnerability"`
			Status        string          `json:"status"`
			Justification string          `json:"justification"`
			Impact        string          `json:"impact_statement"`
			Products      []struct {
				Subcomponents []struct {
					ID          string            `json:"@id"`
					Identifiers map[string]string `json:"identifiers"`
				} `json:"subcomponents"`
			} `json:"products"`
		} `json:"statements"`
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
		return parseVEX(inner, depth+1)
	}
	if len(v.Predicate) > 0 && v.Predicate[0] == '{' {
		return parseVEX(v.Predicate, depth+1)
	}
	var out []VEXStatement
	for _, s := range v.Statements {
		st := VEXStatement{Status: s.Status, Justification: s.Justification, Impact: truncate(s.Impact, 300), Author: truncate(v.Author, 100)}
		var name struct {
			Name string `json:"name"`
		}
		if json.Unmarshal(s.Vulnerability, &st.Vulnerability) != nil { // v0.2: an object; v0.0.1: the ID
			if json.Unmarshal(s.Vulnerability, &name) == nil {
				st.Vulnerability = name.Name
			}
		}
		for _, p := range s.Products {
			for _, sc := range p.Subcomponents {
				id := sc.ID
				if purl := sc.Identifiers["purl"]; purl != "" {
					id = purl
				}
				if strings.HasPrefix(id, "pkg:") {
					st.Packages = append(st.Packages, id)
				}
			}
		}
		if st.Vulnerability != "" && st.Status != "" {
			out = mergeVEX(out, []VEXStatement{st})
		}
	}
	return out
}
