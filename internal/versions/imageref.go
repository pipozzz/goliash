// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package versions

import "strings"

// ImageRef is a parsed container image reference.
type ImageRef struct {
	Registry   string // docker.io, ghcr.io, 123.dkr.ecr.eu-west-1.amazonaws.com, localhost:5000
	Repository string // library/nginx, acme/payments-api
	Tag        string // "latest" when the reference has neither tag nor digest
	Digest     string // sha256:…, when pinned in the reference
}

// Repo returns registry/repository, the identity of an image regardless of version.
func (r ImageRef) Repo() string { return r.Registry + "/" + r.Repository }

// Name returns the last path segment of the repository ("payments-api").
func (r ImageRef) Name() string {
	return r.Repository[strings.LastIndex(r.Repository, "/")+1:]
}

// ParseImage parses references the way Docker does: "nginx" is
// docker.io/library/nginx:latest, the first path component is a registry only when
// it contains a dot or a colon or is "localhost".
func ParseImage(ref string) ImageRef {
	var r ImageRef
	rest := ref
	if before, digest, ok := strings.Cut(rest, "@"); ok {
		rest, r.Digest = before, digest
	}
	if i := strings.LastIndex(rest, ":"); i > strings.LastIndex(rest, "/") {
		rest, r.Tag = rest[:i], rest[i+1:]
	}
	if first, remainder, ok := strings.Cut(rest, "/"); ok &&
		(strings.ContainsAny(first, ".:") || first == "localhost") {
		r.Registry, r.Repository = first, remainder
	} else {
		r.Registry, r.Repository = "docker.io", rest
	}
	if r.Registry == "index.docker.io" || r.Registry == "registry-1.docker.io" {
		r.Registry = "docker.io"
	}
	if r.Registry == "docker.io" && !strings.Contains(r.Repository, "/") {
		r.Repository = "library/" + r.Repository
	}
	if r.Tag == "" && r.Digest == "" {
		r.Tag = "latest"
	}
	return r
}
