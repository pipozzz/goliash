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

// Plan makes up the files the demo repository would have (a Kustomization, a Nomad job or a Compose file, as the
// path says) and their change: one file per path, with every image the bump changes there.
func (Demo) Plan(_ context.Context, b Bump) (Plan, error) {
	base := b.Branch
	if base == "" || base == "HEAD" {
		base = "main"
	}
	var order []string
	byFile := map[string][]Pin{}
	for _, p := range b.Pins() {
		file := strings.Trim(p.Path, "/")
		if !deployFile(file) {
			file += "/kustomization.yaml"
		}
		if _, ok := byFile[file]; !ok {
			order = append(order, file)
		}
		byFile[file] = append(byFile[file], p)
	}
	plan := Plan{Base: base}
	for _, file := range order {
		old := demoFile(file, byFile[file])
		cur := []byte(old)
		for _, p := range byFile[file] {
			cur = versions.BumpImageTag(cur, p.Image, p.From, p.To).Content
		}
		plan.Changes = append(plan.Changes, Change{Path: file, Old: []byte(old), New: cur})
	}
	return plan, nil
}

// demoFile is the demo repository's file pinning the images: a Nomad job, a Compose file or a Kustomization.
func demoFile(file string, pins []Pin) string {
	name := func(p Pin) string { return p.Image[strings.LastIndex(p.Image, "/")+1:] }
	var b strings.Builder
	switch {
	case strings.HasSuffix(file, ".hcl"):
		n := name(pins[0])
		fmt.Fprintf(&b, "job %q {\n  group %q {\n", n, n)
		for _, p := range pins {
			fmt.Fprintf(&b, "    task %q {\n      driver = \"docker\"\n      config {\n        image = \"%s:%s\"\n      }\n    }\n", name(p), p.Image, p.From)
		}
		b.WriteString("  }\n}\n")
	case strings.HasSuffix(file, "/kustomization.yaml"):
		b.WriteString("apiVersion: kustomize.config.k8s.io/v1beta1\nkind: Kustomization\nresources:\n")
		for _, p := range pins {
			fmt.Fprintf(&b, "  - ../../base/%s\n", name(p))
		}
		b.WriteString("images:\n")
		for _, p := range pins {
			fmt.Fprintf(&b, "  - name: %s\n    newTag: %q\n", p.Image, p.From)
		}
	default:
		b.WriteString("services:\n")
		for _, p := range pins {
			fmt.Fprintf(&b, "  %s:\n    image: %s:%s\n    restart: unless-stopped\n", name(p), p.Image, p.From)
		}
	}
	return b.String()
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

// PullChecks says the demo's pull requests pass their checks.
func (Demo) PullChecks(context.Context, string) (string, error) { return "passing", nil }
