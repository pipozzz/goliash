// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package versions

import (
	"strconv"
	"strings"
)

// stepRule is how far one upgrade of a product may go.
type stepRule string

const (
	// stepMinor: one minor version at a time (Kubernetes: 1.31 → 1.32 → 1.33).
	stepMinor stepRule = "minor"
	// stepMajor: the newest release of the running major first, then one major at a time, each at its newest
	// release (Nextcloud: 27.1.3 → 27.1.11 → 28.0.14 → 29.0.9).
	stepMajor stepRule = "major"
)

// stepRules are the products whose upgrades cannot skip versions, by repository.
var stepRules = map[string]stepRule{
	"eks.amazonaws.com/kubernetes":                      stepMinor,
	"docker.io/rancher/k3s":                             stepMinor,
	"docker.io/library/nextcloud":                       stepMajor,
	"lscr.io/linuxserver/nextcloud":                     stepMajor,
	"ghcr.io/linuxserver/nextcloud":                     stepMajor,
	"docker.io/linuxserver/nextcloud":                   stepMajor,
	"docker.elastic.co/elasticsearch/elasticsearch":     stepMajor,
	"docker.io/library/elasticsearch":                   stepMajor,
	"docker.io/opensearchproject/opensearch":            stepMajor,
	"public.ecr.aws/opensearchproject/opensearch":       stepMajor,
	"opensearch.amazonaws.com/opensearch":               stepMajor,
	"opensearch.amazonaws.com/elasticsearch":            stepMajor,
	"docker.elastic.co/kibana/kibana":                   stepMajor,
	"docker.io/library/kibana":                          stepMajor,
	"docker.io/opensearchproject/opensearch-dashboards": stepMajor,
}

// StepNote says why an upgrade path has stops, for its repository: "one minor version at a time".
func StepNote(repo string) string {
	switch stepRules[repo] {
	case stepMinor:
		return "one minor version at a time"
	case stepMajor:
		return "the newest release of each major version on the way"
	}
	return ""
}

// UpgradePath is the versions an upgrade of repo from running to target goes through, target last, when the
// product cannot skip versions; nil when it can go straight there. released are the versions known to be out:
// each stop is the newest of them in its line (a minor or a major), or the line itself when none is known.
func UpgradePath(repo, running, target string, released []string) []string {
	rule := stepRules[repo]
	from, ok1 := ParseVersion(strings.TrimPrefix(running, "v"))
	to, ok2 := ParseVersion(strings.TrimPrefix(target, "v"))
	if rule == "" || !ok1 || !ok2 || to.Compare(from) <= 0 {
		return nil
	}
	newest := map[[2]int]Version{} // the newest release of each minor (and of each major, under minor -1)
	for _, r := range released {
		v, ok := ParseVersion(strings.TrimPrefix(r, "v"))
		if !ok || v.Pre != "" || v.Variant != to.Variant || v.Line || v.Compare(to) > 0 {
			continue // a prerelease, another flavour (alpine, fpm) or past the target is no stop
		}
		v.Raw = r
		for _, k := range [][2]int{{v.part(0), v.part(1)}, {v.part(0), -1}} {
			if cur, seen := newest[k]; !seen || v.Compare(cur) > 0 {
				newest[k] = v
			}
		}
	}
	stop := func(k [2]int, fallback string) string {
		if v, ok := newest[k]; ok {
			return v.Raw
		}
		return fallback
	}
	var path []string
	switch rule {
	case stepMinor:
		if to.part(0) != from.part(0) {
			return nil // a new major of Kubernetes: no rule to follow
		}
		for m := from.part(1) + 1; m < to.part(1); m++ {
			path = append(path, stop([2]int{from.part(0), m}, strconv.Itoa(from.part(0))+"."+strconv.Itoa(m)))
		}
	case stepMajor:
		if to.part(0) == from.part(0) {
			return nil
		}
		if v, ok := newest[[2]int{from.part(0), -1}]; ok && v.Compare(from) > 0 {
			path = append(path, v.Raw)
		}
		for m := from.part(0) + 1; m < to.part(0); m++ {
			path = append(path, stop([2]int{m, -1}, strconv.Itoa(m)))
		}
	}
	if len(path) == 0 {
		return nil
	}
	return append(path, target)
}
