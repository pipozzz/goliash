// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"context"
	"testing"
)

func TestImageEvidence(t *testing.T) {
	forEachDialect(t, func(t *testing.T, s *Store) {
		ctx := context.Background()
		f := setup(t, s)
		sc := f.ws.Scope()
		ev := ImageEvidence{Repo: "docker.io/library/nginx", Digest: "sha256:a", SBOM: true, Provenance: true, Found: []string{"buildkit sbom", "buildkit provenance"}}
		if err := s.SetImageEvidence(ctx, sc, ev); err != nil {
			t.Fatal(err)
		}
		ev.Signed, ev.Found = true, append(ev.Found, "cosign signature")
		if err := s.SetImageEvidence(ctx, sc, ev); err != nil {
			t.Fatal(err)
		}
		if err := s.SetImageEvidence(ctx, sc, ImageEvidence{Repo: "ghcr.io/acme/x", Digest: "sha256:b", Error: "denied"}); err != nil {
			t.Fatal(err)
		}
		got, err := s.ListImageEvidence(ctx, sc)
		if err != nil {
			t.Fatal(err)
		}
		n := got["docker.io/library/nginx@sha256:a"]
		if len(got) != 2 || !n.Signed || !n.SBOM || !n.Provenance || len(n.Found) != 3 || n.CheckedAt.IsZero() {
			t.Fatalf("evidence %+v", got)
		}
		if x := got["ghcr.io/acme/x@sha256:b"]; x.Error != "denied" || x.Signed || len(x.Found) != 0 {
			t.Fatalf("failed lookup %+v", x)
		}
	})
}
