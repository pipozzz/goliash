// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: Apache-2.0

// Package catalog holds Goliash's global catalog of public images: default version
// policies and where release notes live. The data is images.yaml in this directory.
package catalog

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"
)

//go:embed images.yaml
var data []byte

// Entry describes one public image.
type Entry struct {
	Image           string         `yaml:"image"`
	GitHub          string         `yaml:"github"`
	GitLab          string         `yaml:"gitlab"` // host/group/project
	EOL             string         `yaml:"eol"`    // endoflife.date product
	GitHubTagPrefix string         `yaml:"github_tag_prefix"`
	Changelog       string         `yaml:"changelog"`
	Policy          map[string]any `yaml:"policy"`
}

// PolicyJSON returns the entry's default policy as stored on services.
func (e Entry) PolicyJSON() json.RawMessage {
	if len(e.Policy) == 0 {
		return json.RawMessage(`{}`)
	}
	b, _ := json.Marshal(e.Policy)
	return b
}

// ChangelogURL returns the changelog link for a version, when the entry has a template.
func (e Entry) ChangelogURL(version string) string {
	if e.Changelog == "" {
		return ""
	}
	return strings.ReplaceAll(e.Changelog, "{version}", version)
}

var (
	once    sync.Once
	entries map[string]Entry
	loadErr error
)

func load() {
	var list []Entry
	if loadErr = yaml.Unmarshal(data, &list); loadErr != nil {
		loadErr = fmt.Errorf("catalog: %w", loadErr)
		return
	}
	entries = make(map[string]Entry, len(list))
	for _, e := range list {
		if _, dup := entries[e.Image]; dup {
			loadErr = fmt.Errorf("catalog: %s listed twice", e.Image)
			return
		}
		entries[e.Image] = e
	}
}

// Lookup returns the entry for an image repository ("docker.io/library/nginx").
func Lookup(repo string) (Entry, bool) {
	once.Do(load)
	e, ok := entries[repo]
	return e, ok
}

// All returns every entry and the error, if the catalog does not load.
func All() (map[string]Entry, error) {
	once.Do(load)
	return entries, loadErr
}
