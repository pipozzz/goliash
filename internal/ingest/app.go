// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package ingest

import "strings"

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
			return v, k
		}
	}
	if kind == "nomad_job" && name != "" {
		return name, "nomad job"
	}
	if namespace != "" {
		return namespace, "namespace"
	}
	return "", ""
}
