// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package versions

import (
	"strings"

	"github.com/pipozzz/goliash/catalog"
	"github.com/pipozzz/goliash/internal/store"
)

// ServiceKind is a service's kind, own or third_party: the one a person chose, else
// worked out from its image. Docker Hub's official images and the images of the
// catalog are someone else's software; the rest is taken as the team's own.
func ServiceKind(svc store.Service, repo string) string {
	if svc.KindChosen {
		return svc.Kind
	}
	if svc.Upstream != "" {
		repo = svc.Upstream
	}
	repo = ParseImage(repo).Repo()
	if strings.HasPrefix(repo, "docker.io/library/") {
		return "third_party"
	}
	if _, ok := catalog.Lookup(repo); ok {
		return "third_party"
	}
	return "own"
}
