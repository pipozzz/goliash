// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package gitops

import (
	"regexp"
	"strings"
)

// Pull is an open pull or merge request in a GitOps repository, opened by anyone.
type Pull struct {
	URL, Title, Branch string
}

// Proposing finds, among a repository's open pull requests, one that someone else (Renovate, Dependabot, a person)
// opened to move the image to the version: its title names the image and the version ("Update ghcr.io/acme/api
// Docker tag to v1.5.0", "Bump api from 1.4.2 to 1.5.0"). Goliash's own (branches goliash/…) are left out.
func Proposing(pulls []Pull, image, to string) (Pull, bool) {
	name := image[strings.LastIndex(image, "/")+1:]
	version := regexp.MustCompile(`(^|[^0-9A-Za-z.])v?` + regexp.QuoteMeta(strings.TrimPrefix(to, "v")) + `($|[^0-9A-Za-z.]|\.$)`)
	for _, p := range pulls {
		if strings.HasPrefix(p.Branch, "goliash/") || name == "" {
			continue
		}
		title := strings.ToLower(p.Title)
		if strings.Contains(title, strings.ToLower(name)) && version.MatchString(p.Title) {
			return p, true
		}
	}
	return Pull{}, false
}
