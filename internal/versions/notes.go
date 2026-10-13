// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package versions

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/pipozzz/goliash/internal/store"
)

// ReleaseNote is what one release says about itself.
type ReleaseNote struct {
	Version     string    `json:"version"`
	URL         string    `json:"url,omitempty"`
	PublishedAt time.Time `json:"published_at,omitzero"`
	Notes       string    `json:"notes"`
}

// ReleaseNotes is what changed between two versions of a service.
type ReleaseNotes struct {
	Source   string        `json:"source,omitempty"`    // github:owner/repo or gitlab:host/project; empty when none is known
	Releases []ReleaseNote `json:"releases"`            // newest first
	Omitted  int           `json:"omitted,omitempty"`   // releases in range left out for length
	Link     string        `json:"changelog,omitempty"` // the changelog of to, when its notes live outside a forge
}

const (
	maxNoteReleases = 25
	maxNotesTotal   = 80 << 10
)

// ErrBadVersion means a version given is not one Goliash can compare.
var ErrBadVersion = errors.New("not a version Goliash can compare")

// ReleaseNotes returns the notes of svc's releases after from, up to and including to, newest first, from the
// GitHub or GitLab releases its policy (or its image's source label, or the catalog) names; a nil checker
// knows no forge. repo is the image
// repository it runs from. An empty from returns the notes of to alone. Pre-releases count only when to is one.
func (c *Checker) ReleaseNotes(ctx context.Context, svc store.Service, repo, from, to string) (ReleaseNotes, error) {
	out := ReleaseNotes{Releases: []ReleaseNote{}}
	top, ok := ParseVersion(to)
	if !ok {
		return out, ErrBadVersion
	}
	var bottom Version
	if from != "" {
		if bottom, ok = ParseVersion(from); !ok {
			return out, ErrBadVersion
		}
	}
	p, _, err := PolicyFor(svc, repo)
	if err != nil {
		return out, err
	}
	var forge []GitHubRelease
	var gh *GitHub
	var gl *GitLab
	if c != nil {
		gh, gl = c.github, c.gitlab
	}
	switch {
	case p.GitHub != "" && gh != nil:
		out.Source = "github:" + p.GitHub
		forge, err = gh.Releases(ctx, p.GitHub)
	case p.GitLab != "" && gl != nil:
		out.Source = "gitlab:" + p.GitLab
		forge, err = gl.Releases(ctx, p.GitLab)
	}
	if err != nil {
		return out, err
	}
	if p.Changelog != "" {
		out.Link = strings.ReplaceAll(p.Changelog, "{version}", versionNumber(top))
	}
	type found struct {
		v Version
		r GitHubRelease
	}
	var in []found
	for _, r := range forge {
		v, ok := ParseVersion(strings.TrimPrefix(r.Tag, p.GitHubTagPrefix))
		if r.Draft || !ok || v.Compare(top) > 0 || (v.Pre != "" && top.Pre == "") {
			continue
		}
		if from == "" && v.Compare(top) != 0 || from != "" && v.Compare(bottom) <= 0 {
			continue
		}
		in = append(in, found{v, r})
	}
	sort.Slice(in, func(i, j int) bool { return in[i].v.Compare(in[j].v) > 0 })
	total := 0
	for i, f := range in {
		if i >= maxNoteReleases || total+len(f.r.Body) > maxNotesTotal {
			out.Omitted = len(in) - i
			break
		}
		total += len(f.r.Body)
		out.Releases = append(out.Releases, ReleaseNote{Version: f.v.Raw, URL: f.r.URL, PublishedAt: f.r.PublishedAt, Notes: f.r.Body})
	}
	return out, nil
}
