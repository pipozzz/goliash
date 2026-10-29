// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package versions

import (
	"testing"
	"time"

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
		// Someone's public image, read anonymously: third party; a company's package the registry refused: own.
		{store.Service{SourceImage: "docker.io/louislam/uptime-kuma", SourceCheckedAt: time.Now()}, "louislam/uptime-kuma", "third_party"},
		{store.Service{SourceImage: "ghcr.io/acme/payments-api", SourceCheckedAt: time.Now(), PrivateUpstream: "ghcr.io/acme/payments-api"}, "ghcr.io/acme/payments-api", "own"},
		{store.Service{SourceImage: "123.dkr.ecr.eu-west-1.amazonaws.com/api", SourceCheckedAt: time.Now()}, "123.dkr.ecr.eu-west-1.amazonaws.com/api", "own"},
	} {
		if got := ServiceKind(c.svc, c.repo); got != c.want {
			t.Errorf("ServiceKind(%q, %q) = %s, want %s", c.svc.Kind, c.repo, got, c.want)
		}
	}
}
