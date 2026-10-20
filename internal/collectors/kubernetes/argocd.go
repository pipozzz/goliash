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

// argoApplications are Argo CD's Application objects. Reading them needs get and list on
// applications.argoproj.io; without the CRD or the permission there is simply no GitOps
// information, never an incomplete snapshot.
var argoApplications = schema.GroupVersionResource{Group: "argoproj.io", Version: "v1alpha1", Resource: "applications"}

// argoKinds are the Argo CD resource kinds that are workloads here.
var argoKinds = map[string]agentproto.WorkloadKind{
	"Deployment": agentproto.Deployment, "StatefulSet": agentproto.Statefulset,
	"DaemonSet": agentproto.Daemonset, "CronJob": agentproto.Cronjob,
}

// workloadKey identifies a workload the way an Argo CD Application lists its resources.
type workloadKey struct {
	kind      agentproto.WorkloadKind
	namespace string
	name      string
}

// argoCD reads every Argo CD Application the collector may see and returns, per
// workload it deploys, what the application says about it.
func (c *Collector) argoCD(ctx context.Context) map[workloadKey]agentproto.GitOps {
	if c.dyn == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	list, err := c.dyn.Resource(argoApplications).Namespace(metav1.NamespaceAll).List(ctx, metav1.ListOptions{})
	if err != nil { // no Argo CD here, no permission, or a passing failure: the workloads still count
		return nil
	}
	windows := c.argoSyncWindows(ctx)
	now := c.clock()
	out := map[workloadKey]agentproto.GitOps{}
	for _, app := range list.Items {
		g := argoGitOps(app)
		blockedBy(&g, app, windows, now)
		resources, _, _ := unstructured.NestedSlice(app.Object, "status", "resources")
		for _, r := range resources {
			res, ok := r.(map[string]any)
			if !ok {
				continue
			}
			kind, known := argoKinds[str(res["kind"])]
			if !known {
				continue
			}
			out[workloadKey{kind, str(res["namespace"]), str(res["name"])}] = g
		}
	}
	return out
}

// argoGitOps reads one Application: where it deploys from and its sync and health.
func argoGitOps(app unstructured.Unstructured) agentproto.GitOps {
	g := agentproto.GitOps{Tool: agentproto.Argocd, App: app.GetName()}
	set := func(dst **string, v string) {
		if v != "" {
			*dst = &v
		}
	}
	set(&g.AppNamespace, app.GetNamespace())
	project, _, _ := unstructured.NestedString(app.Object, "spec", "project")
	set(&g.Project, project)
	source, _, _ := unstructured.NestedMap(app.Object, "spec", "source")
	if source == nil { // a multi-source application: the first source with a path or chart
		sources, _, _ := unstructured.NestedSlice(app.Object, "spec", "sources")
		for _, s := range sources {
			if m, ok := s.(map[string]any); ok && (str(m["path"]) != "" || str(m["chart"]) != "") {
				source = m
				break
			}
		}
	}
	if source != nil {
		set(&g.Repo, str(source["repoURL"]))
		path := str(source["path"])
		if path == "" {
			path = str(source["chart"])
		}
		set(&g.Path, path)
		set(&g.TargetRevision, str(source["targetRevision"]))
	}
	sync, _, _ := unstructured.NestedString(app.Object, "status", "sync", "status")
	set(&g.Sync, sync)
	revision, _, _ := unstructured.NestedString(app.Object, "status", "sync", "revision")
	if revision == "" {
		if revs, _, _ := unstructured.NestedStringSlice(app.Object, "status", "sync", "revisions"); len(revs) > 0 {
			revision = revs[0]
		}
	}
	set(&g.Revision, revision)
	health, _, _ := unstructured.NestedString(app.Object, "status", "health", "status")
	set(&g.Health, health)
	return g
}

// blockedBy sets whether the sync windows of the application's project keep it from syncing at now.
func blockedBy(g *agentproto.GitOps, app unstructured.Unstructured, windows map[string][]syncWindow, now time.Time) {
	project := "default"
	if g.Project != nil {
		project = *g.Project
	}
	if len(windows[project]) == 0 {
		return
	}
	dest, _, _ := unstructured.NestedStringMap(app.Object, "spec", "destination")
	why, until := syncBlocked(windows[project], now, app.GetName(), dest["namespace"], dest["server"], dest["name"])
	if why == "" {
		return
	}
	g.SyncBlocked = &why
	if !until.IsZero() {
		g.SyncBlockedUntil = &until
	}
}

func str(v any) string {
	s, _ := v.(string)
	return strings.TrimSpace(s)
}
