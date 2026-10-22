// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: Apache-2.0

package registry

import (
	"strings"
	"testing"
)

func TestProvenanceImages(t *testing.T) {
	v02 := `{"predicateType":"https://slsa.dev/provenance/v0.2","predicate":{"materials":[
		{"uri":"pkg:docker/golang@1.23?platform=linux%2Famd64","digest":{"sha256":"aaa"}},
		{"uri":"pkg:docker/docker.io/library/alpine@3.20?platform=linux%2Famd64","digest":{"sha256":"bbb"}},
		{"uri":"https://github.com/acme/app.git#main","digest":{"sha1":"ccc"}}]}}`
	if got := strings.Join(provenanceImages([]byte(v02)), " "); got != "golang:1.23@sha256:aaa docker.io/library/alpine:3.20@sha256:bbb" {
		t.Errorf("v0.2: %s", got)
	}
	v1 := `{"predicateType":"https://slsa.dev/provenance/v1","predicate":{"buildDefinition":{"resolvedDependencies":[
		{"uri":"pkg:docker/ghcr.io/acme/base@1?platform=linux%2Farm64","digest":{"sha256":"ddd"}}]}}}`
	if got := strings.Join(provenanceImages([]byte(v1)), " "); got != "ghcr.io/acme/base:1@sha256:ddd" {
		t.Errorf("v1: %s", got)
	}
	if got := dockerPurl("pkg:docker/alpine@sha256:eee", "eee"); got != "alpine@sha256:eee" {
		t.Errorf("by digest: %s", got)
	}
	if provenanceImages([]byte("not json")) != nil {
		t.Error("garbage gave images")
	}
}
