// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: Apache-2.0

package kubernetes

import (
	"context"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/pipozzz/goliash/pkg/agentproto"
)

// Flux's objects. Reading them needs get and list; without the CRDs or the permission there is no GitOps
// information, never an incomplete snapshot.
var (
	fluxKustomizations  = schema.GroupVersionResource{Group: "kustomize.toolkit.fluxcd.io", Version: "v1", Resource: "kustomizations"}
	fluxHelmReleases    = schema.GroupVersionResource{Group: "helm.toolkit.fluxcd.io", Version: "v2", Resource: "helmreleases"}
	fluxGitRepositories = schema.GroupVersionResource{Group: "source.toolkit.fluxcd.io", Version: "v1", Resource: "gitrepositories"}
	fluxHelmRepos       = schema.GroupVersionResource{Group: "source.toolkit.fluxcd.io", Version: "v1", Resource: "helmrepositories"}
)

// Flux labels what it applies with its Kustomization's or HelmRelease's name and namespace.
const (
	fluxKustomizationName = "kustomize.toolkit.fluxcd.io/name"
	fluxKustomizationNS   = "kustomize.toolkit.fluxcd.io/namespace"
	fluxHelmReleaseName   = "helm.toolkit.fluxcd.io/name"
	fluxHelmReleaseNS     = "helm.toolkit.fluxcd.io/namespace"
)

// fluxObjects is what the collector read of Flux: its Kustomizations and HelmReleases by namespace/name, as
// GitOps information to give the workloads they labelled.
type fluxObjects struct {
	kustomizations map[string]agentproto.GitOps
	helmReleases   map[string]agentproto.GitOps
}

// flux reads Flux's Kustomizations, HelmReleases and their sources, when the cluster has them.
func (c *Collector) flux(ctx context.Context) fluxObjects {
	out := fluxObjects{kustomizations: map[string]agentproto.GitOps{}, helmReleases: map[string]agentproto.GitOps{}}
	if c.dyn == nil {
		return out
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	list := func(gvr schema.GroupVersionResource) []unstructured.Unstructured {
		l, err := c.dyn.Resource(gvr).Namespace(metav1.NamespaceAll).List(ctx, metav1.ListOptions{})
		if err != nil { // no Flux, no permission or a passing failure: the workloads still count
			return nil
		}
		return l.Items
	}
	ks, hrs := list(fluxKustomizations), list(fluxHelmReleases)
	if len(ks) == 0 && len(hrs) == 0 {
		return out
	}
	type source struct{ url, ref, revision string }
	sources := map[string]source{} // kind/namespace/name
	for _, r := range list(fluxGitRepositories) {
		s := source{url: nested(r, "spec", "url"), revision: nested(r, "status", "artifact", "revision")}
		for _, k := range []string{"branch", "tag", "semver", "name", "commit"} {
			if v := nested(r, "spec", "ref", k); v != "" {
				s.ref = v
				break
			}
		}
		sources["GitRepository/"+r.GetNamespace()+"/"+r.GetName()] = s
	}
	for _, r := range list(fluxHelmRepos) {
		sources["HelmRepository/"+r.GetNamespace()+"/"+r.GetName()] = source{url: nested(r, "spec", "url")}
	}
	sourceOf := func(obj unstructured.Unstructured, path ...string) source {
		kind, name := nested(obj, append(path, "kind")...), nested(obj, append(path, "name")...)
		ns := nested(obj, append(path, "namespace")...)
		if ns == "" {
			ns = obj.GetNamespace()
		}
		return sources[kind+"/"+ns+"/"+name]
	}
	for _, k := range ks {
		src := sourceOf(k, "spec", "sourceRef")
		applied := nested(k, "status", "lastAppliedRevision")
		g := fluxGitOps(k, src.url, nested(k, "spec", "path"), src.ref, applied)
		sync := "Synced"
		if src.revision != "" && applied != src.revision { // the source has a commit not applied yet
			sync = "OutOfSync"
		}
		g.Sync = &sync
		out.kustomizations[k.GetNamespace()+"/"+k.GetName()] = g
	}
	for _, h := range hrs {
		src := sourceOf(h, "spec", "chart", "spec", "sourceRef")
		chart := nested(h, "spec", "chart", "spec", "chart")
		applied := nested(h, "status", "lastAppliedRevision")
		g := fluxGitOps(h, src.url, chart, nested(h, "spec", "chart", "spec", "version"), applied)
		sync := "Synced"
		if attempted := nested(h, "status", "lastAttemptedRevision"); attempted != "" && attempted != applied {
			sync = "OutOfSync"
		}
		g.Sync = &sync
		out.helmReleases[h.GetNamespace()+"/"+h.GetName()] = g
	}
	return out
}

// fluxGitOps describes a Kustomization or HelmRelease: where from, what it follows, the revision applied, and its
// health from its Ready condition.
func fluxGitOps(obj unstructured.Unstructured, repo, path, ref, applied string) agentproto.GitOps {
	g := agentproto.GitOps{Tool: agentproto.Flux, App: obj.GetName()}
	set := func(dst **string, v string) {
		if v != "" {
			*dst = &v
		}
	}
	set(&g.AppNamespace, obj.GetNamespace())
	set(&g.Repo, repo)
	set(&g.Path, strings.TrimPrefix(path, "./"))
	set(&g.TargetRevision, ref)
	// "main@sha1:4f1c2e9…" (Flux 2.x), "main/4f1c2e9…" (older): the commit; a chart version stays as it is.
	rev := applied
	if _, after, ok := strings.Cut(rev, "@sha1:"); ok {
		rev = after
	} else if _, after, ok := strings.Cut(rev, "@sha256:"); ok {
		rev = after
	} else if i := strings.LastIndex(rev, "/"); i >= 0 && len(rev)-i > 7 {
		rev = rev[i+1:]
	}
	set(&g.Revision, rev)
	health := "Unknown"
	if suspended, _, _ := unstructured.NestedBool(obj.Object, "spec", "suspend"); suspended {
		health = "Suspended"
		why := "suspended in Flux (spec.suspend): it applies nothing until resumed"
		g.SyncBlocked = &why
	} else {
		conds, _, _ := unstructured.NestedSlice(obj.Object, "status", "conditions")
		for _, c := range conds {
			m, ok := c.(map[string]any)
			if !ok || str(m["type"]) != "Ready" {
				continue
			}
			switch {
			case str(m["status"]) == "True":
				health = "Healthy"
			case str(m["reason"]) == "Progressing" || str(m["reason"]) == "DependencyNotReady" || str(m["status"]) == "Unknown":
				health = "Progressing"
			default:
				health = "Degraded"
			}
		}
	}
	set(&g.Health, health)
	return g
}

// fluxFor is the GitOps information of the Kustomization or HelmRelease that labelled a workload; a HelmRelease
// wins, as it is the closer owner.
func (f fluxObjects) fluxFor(labels map[string]string, ns string) *agentproto.GitOps {
	at := func(name, namespace string) string {
		if namespace == "" {
			namespace = ns
		}
		return namespace + "/" + name
	}
	if name := labels[fluxHelmReleaseName]; name != "" {
		if g, ok := f.helmReleases[at(name, labels[fluxHelmReleaseNS])]; ok {
			return &g
		}
	}
	if name := labels[fluxKustomizationName]; name != "" {
		if g, ok := f.kustomizations[at(name, labels[fluxKustomizationNS])]; ok {
			return &g
		}
	}
	return nil
}

func nested(obj unstructured.Unstructured, path ...string) string {
	v, _, _ := unstructured.NestedString(obj.Object, path...)
	return strings.TrimSpace(v)
}
