// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package versions

import (
	"testing"

	"github.com/pipozzz/goliash/internal/store"
)

func TestServiceKind(t *testing.T) {
	for _, c := range []struct {
		svc  store.Service
		repo string
		want string
	}{
		{store.Service{}, "docker.io/library/postgres", "third_party"},
		{store.Service{}, "postgres", "third_party"}, // Docker Hub's short name
		{store.Service{}, "ghcr.io/acme/payments-api", "own"},
		{store.Service{Kind: "own", KindChosen: true}, "docker.io/library/postgres", "own"}, // a person's choice stands
		{store.Service{Kind: "third_party", KindChosen: true}, "ghcr.io/acme/payments-api", "third_party"},
		{store.Service{Kind: "own"}, "docker.io/library/postgres", "third_party"}, // created own, never chosen
		{store.Service{Upstream: "docker.io/library/redis"}, "registry.acme.internal/mirror/redis", "third_party"},
	} {
		if got := ServiceKind(c.svc, c.repo); got != c.want {
			t.Errorf("ServiceKind(%q, %q) = %s, want %s", c.svc.Kind, c.repo, got, c.want)
		}
	}
}
