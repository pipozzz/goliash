// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package ingest

import (
	"strings"

	"github.com/pipozzz/goliash/internal/mapping"
)

// appLabels are the labels that name the application a workload belongs to, most
// specific first: Goliash's own, the Kubernetes recommended labels (part-of groups
// the components of one application; instance is usually the Helm release), the
// older Helm release label, and Compose and Swarm projects.
var appLabels = []string{
	"goliash.app",
	"app.kubernetes.io/part-of",
	"app.kubernetes.io/instance",
	"release",
	"com.docker.compose.project",
	"com.docker.stack.namespace",
}

// workloadApp names the application of a workload and says where the name came
// from: custom (a workspace's own label key, tried first), one of appLabels, or the
// namespace when no label names it. A Nomad job is an application of its own, so
// without a label its name stands in, not the (usually shared) namespace.
func workloadApp(labels map[string]string, custom, namespace, kind, name string) (app, source string) {
	if custom != "" {
		if v := strings.TrimSpace(labels[custom]); v != "" {
			return v, custom
		}
	}
	for _, k := range appLabels {
		if v := strings.TrimSpace(labels[k]); v != "" {
			if k == "com.docker.compose.project" || k == "com.docker.stack.namespace" {
				v = withoutGeneratedSuffix(v)
			}
			return v, k
		}
	}
	if kind == "nomad_job" && name != "" {
		return withoutGeneratedSuffix(name), "nomad job"
	}
	if namespace != "" {
		return withoutGeneratedSuffix(namespace), "namespace"
	}
	return "", ""
}

// withoutGeneratedSuffix drops the random suffix platforms add to the names they
// generate (see mapping.StableName).
func withoutGeneratedSuffix(name string) string { return mapping.StableName(name) }

// teamLabels name the team that owns a workload, most specific first.
var teamLabels = []string{"goliash.team", "team", "owner", "app.kubernetes.io/team"}

// workloadTeam is the team a workload's labels name, if any.
func workloadTeam(labels map[string]string) string {
	for _, k := range teamLabels {
		if v := strings.TrimSpace(labels[k]); v != "" {
			return v
		}
	}
	return ""
}
