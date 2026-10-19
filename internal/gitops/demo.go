// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package gitops

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/pipozzz/goliash/internal/versions"
)

// ErrDemo is returned by the demo when asked to open a pull request: it shows what it would open and stops.
var ErrDemo = errors.New("the demo shows the pull request it would open, and opens none")

// Demo plays a GitOps repository for the demo data (github.com/acme/…): a Kustomization pinning the running
// version under the application's path, so the preview shows a real change. It opens nothing.
type Demo struct{}

// Supports says whether the repository is one of the demo's.
func (Demo) Supports(repo string) bool {
	u, err := url.Parse(repo)
	return err == nil && u.Host == "github.com" && strings.HasPrefix(u.Path, "/acme/")
}

// Plan makes up the Kustomization the demo repository would have, and its change.
func (Demo) Plan(_ context.Context, b Bump) (Plan, error) {
	name := b.Image[strings.LastIndex(b.Image, "/")+1:]
	old := fmt.Sprintf(`apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
namespace: shop
resources:
  - ../../base/%s
images:
  - name: %s
    newTag: "%s"
`, name, b.Image, b.From)
	res := versions.BumpImageTag([]byte(old), b.Image, b.From, b.To)
	base := b.Branch
	if base == "" || base == "HEAD" {
		base = "main"
	}
	return Plan{Base: base, Changes: []Change{{Path: strings.Trim(b.Path, "/") + "/kustomization.yaml", Old: []byte(old), New: res.Content}}}, nil
}

// OpenBump opens nothing in the demo.
func (Demo) OpenBump(context.Context, Bump) (string, error) { return "", ErrDemo }

// PullState says every demo pull request is open: the demo opens none, so none changes.
func (Demo) PullState(context.Context, string) (string, error) { return "open", nil }

// demoPins is what the demo's repository pins where it matters to the story: the webshop's staging application
// waits for a manual sync of payments-api 1.7.0.
var demoPins = map[string][]string{"apps/webshop/staging|ghcr.io/acme/payments-api": {"1.7.0"}}

// Pinned says what the demo repository pins for the story's application, and knows nothing else.
func (Demo) Pinned(_ context.Context, b Bump) ([]string, error) {
	return demoPins[strings.Trim(b.Path, "/")+"|"+b.Image], nil
}

// OpenPulls makes up Renovate's pull request in the demo repository: payments-api 1.7.0 waits for review there.
func (Demo) OpenPulls(context.Context, string) ([]Pull, error) {
	return []Pull{{
		URL: "https://github.com/acme/deploy/pull/412", Title: "Update ghcr.io/acme/payments-api Docker tag to v1.7.0", Branch: "renovate/payments-api-1.x",
	}}, nil
}
