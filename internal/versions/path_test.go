// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package versions

import (
	"slices"
	"testing"
)

func TestUpgradePath(t *testing.T) {
	eks := []string{"1.29", "1.30", "1.31", "1.32", "1.33", "1.34"}
	gitlab := []string{"17.3.1-ce.0", "17.3.7-ce.0", "17.5.1-ce.0", "17.5.5-ce.0", "17.11.6-ce.0", "17.11.6-ee.0", "18.2.8-ce.0", "18.4.1-ce.0"}
	nextcloud := []string{"27.1.3", "27.1.11", "28.0.1", "28.0.14", "28.0.14-fpm", "29.0.0rc1", "29.0.8", "29.0.9", "30.0.1", "30.0.2", "31.0.0-beta1"}
	for _, c := range []struct {
		repo, running, target string
		released              []string
		want                  []string
	}{
		{"eks.amazonaws.com/kubernetes", "1.31", "1.34", eks, []string{"1.32", "1.33", "1.34"}},
		{"eks.amazonaws.com/kubernetes", "1.33", "1.34", eks, nil},                      // one step: straight there
		{"eks.amazonaws.com/kubernetes", "1.30", "1.32", nil, []string{"1.31", "1.32"}}, // releases unknown: the line
		{"docker.io/rancher/k3s", "v1.30.4-k3s1", "v1.32.2-k3s1", []string{"v1.31.1-k3s1", "v1.31.6-k3s1", "v1.32.2-k3s1"}, []string{"v1.31.6-k3s1", "v1.32.2-k3s1"}},
		{"docker.io/library/nextcloud", "27.1.3", "30.0.2", nextcloud, []string{"27.1.11", "28.0.14", "29.0.9", "30.0.2"}},
		{"docker.io/library/nextcloud", "29.0.9", "30.0.2", nextcloud, nil}, // newest of its major: straight on
		{"docker.io/library/nextcloud", "29.0.8", "30.0.2", nextcloud, []string{"29.0.9", "30.0.2"}},
		{"docker.io/library/nextcloud", "28.0.1", "28.0.14", nextcloud, nil}, // within a major
		{"docker.io/library/nginx", "1.25.1", "1.27.2", nil, nil},            // no rule
		{"docker.io/gitlab/gitlab-ce", "17.2.4-ce.0", "18.4.1-ce.0", gitlab, []string{"17.3.7-ce.0", "17.5.5-ce.0", "17.8", "17.11.6-ce.0", "18.2.8-ce.0", "18.4.1-ce.0"}},
		{"docker.io/gitlab/gitlab-ce", "17.5.1-ce.0", "17.7.0-ce.0", gitlab, []string{"17.5.5-ce.0", "17.7.0-ce.0"}}, // the stop's newest patch first
		{"docker.io/gitlab/gitlab-ce", "17.5.5-ce.0", "17.7.0-ce.0", gitlab, nil},
		{"docker.io/gitlab/gitlab-ee", "20.0.1-ee.0", "20.6.0-ee.0", nil, []string{"20.2", "20.5", "20.6.0-ee.0"}}, // past the list: x.2, x.5
	} {
		if got := UpgradePath(c.repo, c.running, c.target, c.released); !slices.Equal(got, c.want) {
			t.Errorf("%s %s → %s: %v, want %v", c.repo, c.running, c.target, got, c.want)
		}
	}
}
