// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: Apache-2.0

package kubernetes

import (
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/pipozzz/goliash/pkg/agentproto"
)

func fluxObj(apiVersion, kind, ns, name string, spec, status map[string]any) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": apiVersion, "kind": kind, "metadata": map[string]any{"name": name, "namespace": ns},
		"spec": spec, "status": status,
	}}
}

func ready(status, reason string) []any {
	return []any{map[string]any{"type": "Ready", "status": status, "reason": reason}}
}

// Workloads Flux applied carry the Kustomization's or HelmRelease's source, revision, sync and health.
func TestFlux(t *testing.T) {
	objs := []runtime.Object{
		fluxObj("source.toolkit.fluxcd.io/v1", "GitRepository", "flux-system", "deploy",
			map[string]any{"url": "https://github.com/acme/deploy", "ref": map[string]any{"branch": "main"}},
			map[string]any{"artifact": map[string]any{"revision": "main@sha1:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}}),
		fluxObj("kustomize.toolkit.fluxcd.io/v1", "Kustomization", "flux-system", "payments",
			map[string]any{"path": "./apps/payments/prod", "sourceRef": map[string]any{"kind": "GitRepository", "name": "deploy"}},
			map[string]any{"lastAppliedRevision": "main@sha1:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "conditions": ready("False", "ReconciliationFailed")}),
		fluxObj("source.toolkit.fluxcd.io/v1", "HelmRepository", "flux-system", "bitnami",
			map[string]any{"url": "https://charts.bitnami.com/bitnami"}, map[string]any{}),
		fluxObj("helm.toolkit.fluxcd.io/v2", "HelmRelease", "data", "postgres",
			map[string]any{"chart": map[string]any{"spec": map[string]any{
				"chart": "postgresql", "version": "15.5.x",
				"sourceRef": map[string]any{"kind": "HelmRepository", "name": "bitnami", "namespace": "flux-system"},
			}}},
			map[string]any{"lastAppliedRevision": "15.5.0", "lastAttemptedRevision": "15.5.0", "conditions": ready("True", "Succeeded")}),
	}
	dyn := fakeDynamic(objs...)

	fx := fixtures()
	for _, o := range fx {
		switch d := o.(type) {
		case *appsv1.Deployment:
			if d.Name == "payments-api" {
				d.Labels[fluxKustomizationName], d.Labels[fluxKustomizationNS] = "payments", "flux-system"
			}
		case *appsv1.StatefulSet:
			if d.Name == "postgres" {
				d.Labels[fluxHelmReleaseName], d.Labels[fluxHelmReleaseNS] = "postgres", "data"
			}
		}
	}
	c := newCollector(fake.NewClientset(fx...), agentproto.KubernetesSettings{ExcludeNamespaces: []string{"kube-system"}})
	c.dyn = dyn
	start(t, c, func() {})
	got := byName(collect(t, c).Workloads)

	api := got["payments/payments-api"].Gitops
	if api == nil || api.Tool != agentproto.Flux || api.App != "payments" || *api.Repo != "https://github.com/acme/deploy" ||
		*api.Path != "apps/payments/prod" || *api.TargetRevision != "main" || *api.Revision != "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" ||
		*api.Sync != "OutOfSync" || *api.Health != "Degraded" {
		t.Fatalf("payments-api %+v", api)
	}
	pg := got["data/postgres"].Gitops
	if pg == nil || *pg.Path != "postgresql" || *pg.Repo != "https://charts.bitnami.com/bitnami" || *pg.Revision != "15.5.0" ||
		*pg.TargetRevision != "15.5.x" || *pg.Sync != "Synced" || *pg.Health != "Healthy" {
		t.Fatalf("postgres %+v", pg)
	}
	if got["monitoring/node-exporter"].Gitops != nil {
		t.Error("a workload Flux did not label has gitops")
	}
}
