// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: Apache-2.0

package kubernetes

import (
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/pipozzz/goliash/pkg/agentproto"
)

func argoApp(name string, spec, status map[string]any) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "argoproj.io/v1alpha1", "kind": "Application",
		"metadata": map[string]any{"name": name, "namespace": "argocd"},
		"spec":     spec, "status": status,
	}}
}

// Workloads an Argo CD Application lists among its resources carry where they are deployed
// from and whether the application is in sync.
func TestArgoCD(t *testing.T) {
	payments := argoApp("payments-prod", map[string]any{
		"project": "shop",
		"source":  map[string]any{"repoURL": "https://github.com/acme/deploy.git", "path": "apps/payments/prod", "targetRevision": "main"},
	}, map[string]any{
		"sync":   map[string]any{"status": "OutOfSync", "revision": "4f1c2e9"},
		"health": map[string]any{"status": "Degraded"},
		"resources": []any{
			map[string]any{"group": "apps", "kind": "Deployment", "namespace": "payments", "name": "payments-api"},
			map[string]any{"kind": "Service", "namespace": "payments", "name": "payments-api"},
		},
	})
	data := argoApp("postgres", map[string]any{
		"sources": []any{
			map[string]any{"repoURL": "https://github.com/acme/values.git", "ref": "values"},
			map[string]any{"repoURL": "registry-1.docker.io/bitnamicharts", "chart": "postgresql", "targetRevision": "15.5.0"},
		},
	}, map[string]any{
		"sync":      map[string]any{"status": "Synced", "revisions": []any{"abc", "15.5.0"}},
		"health":    map[string]any{"status": "Healthy"},
		"resources": []any{map[string]any{"group": "apps", "kind": "StatefulSet", "namespace": "data", "name": "postgres"}},
	})
	dyn := fakeDynamic(payments, data)

	c := newCollector(fake.NewClientset(fixtures()...), agentproto.KubernetesSettings{ExcludeNamespaces: []string{"kube-system"}})
	c.dyn = dyn
	start(t, c, func() {})
	got := byName(collect(t, c).Workloads)

	api := got["payments/payments-api"].Gitops
	if api == nil || api.App != "payments-prod" || *api.Repo != "https://github.com/acme/deploy.git" || *api.Path != "apps/payments/prod" ||
		*api.TargetRevision != "main" || *api.Revision != "4f1c2e9" || *api.Sync != "OutOfSync" || *api.Health != "Degraded" ||
		*api.Project != "shop" || *api.AppNamespace != "argocd" || api.Tool != agentproto.Argocd {
		t.Fatalf("payments-api gitops %+v", api)
	}
	pg := got["data/postgres"].Gitops
	if pg == nil || *pg.Path != "postgresql" || *pg.TargetRevision != "15.5.0" || *pg.Revision != "abc" || *pg.Sync != "Synced" {
		t.Fatalf("postgres gitops %+v", pg)
	}
	if got["monitoring/node-exporter"].Gitops != nil {
		t.Error("a workload no application lists has gitops")
	}
}

// Without Argo CD (no CRD) the snapshot is complete and has no GitOps information.
func TestNoArgoCD(t *testing.T) {
	c := newCollector(fake.NewClientset(fixtures()...), agentproto.KubernetesSettings{})
	gvr := schema.GroupVersionResource{Group: "argoproj.io", Version: "v1alpha1", Resource: "applications"}
	dyn := fakeDynamic()
	dyn.PrependReactor("list", "applications", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewNotFound(gvr.GroupResource(), "") // the CRD is not installed
	})
	c.dyn = dyn
	start(t, c, func() {})
	res := collect(t, c)
	if !res.Complete {
		t.Fatalf("incomplete: %v", res.Errors)
	}
	for _, w := range res.Workloads {
		if w.Gitops != nil {
			t.Fatalf("%s has gitops", w.Name)
		}
	}
}

// fakeDynamic is a dynamic client that lists Argo CD's and Flux's objects.
func fakeDynamic(objs ...runtime.Object) *dynamicfake.FakeDynamicClient {
	return dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		argoApplications: "ApplicationList", fluxKustomizations: "KustomizationList", fluxHelmReleases: "HelmReleaseList",
		fluxGitRepositories: "GitRepositoryList", fluxHelmRepos: "HelmRepositoryList",
	}, objs...)
}
