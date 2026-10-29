// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package versions

import (
	"strings"

	"github.com/pipozzz/goliash/catalog"
	"github.com/pipozzz/goliash/internal/store"
)

// ServiceKind is a service's kind, own or third_party: the one a person chose, else
// worked out from its image. Docker Hub's official images, the images of the catalog,
// and images any one may pull from a public registry (louislam/uptime-kuma, a project's
// GHCR image) are someone else's software; images in a private registry, or that a public
// registry refused anonymously (a company's private GHCR package), are the team's own.
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
	// Read anonymously: the server looked at the image itself, and the registry did not refuse it.
	if IsPublicRegistry(repo) && svc.PrivateUpstream != repo && svc.SourceImage == repo && !svc.SourceCheckedAt.IsZero() &&
		!LambdaRuntimeOrManaged(repo) {
		return "third_party"
	}
	return "own"
}

// LambdaRuntimeOrManaged reports whether repo is a Lambda runtime or a managed engine: AWS's, not an image.
func LambdaRuntimeOrManaged(repo string) bool { return LambdaRuntime(repo) != "" || Managed(repo) }
