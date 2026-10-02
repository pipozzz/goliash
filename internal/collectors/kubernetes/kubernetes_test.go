// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: Apache-2.0

package kubernetes

import (
	"context"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/pipozzz/goliash/internal/collectors"
	"github.com/pipozzz/goliash/pkg/agentproto"
)

var (
	digestA = "sha256:" + strings.Repeat("a", 64)
	digestB = "sha256:" + strings.Repeat("b", 64)
	digestE = "sha256:" + strings.Repeat("e", 64)
)

func meta(ns, name, uid string, owner *metav1.OwnerReference) metav1.ObjectMeta {
	m := metav1.ObjectMeta{Namespace: ns, Name: name, UID: types.UID(uid), Labels: map[string]string{"app": name}}
	if owner != nil {
		m.OwnerReferences = []metav1.OwnerReference{*owner}
	}
	return m
}

func ownedBy(kind, uid string) *metav1.OwnerReference {
	yes := true
	return &metav1.OwnerReference{Kind: kind, Name: uid, UID: types.UID(uid), Controller: &yes}
}

func podSpec(containers ...string) corev1.PodSpec {
	var spec corev1.PodSpec
	for _, c := range containers {
		name, image, _ := strings.Cut(c, "=")
		spec.Containers = append(spec.Containers, corev1.Container{Name: name, Image: image})
	}
	return spec
}

func runningPod(ns, name, ownerKind, ownerUID string, containers ...string) *corev1.Pod {
	p := &corev1.Pod{ObjectMeta: meta(ns, name, "pod-"+name, ownedBy(ownerKind, ownerUID))}
	p.Status.Phase = corev1.PodRunning
	for _, c := range containers {
		// name=image@imageID
		name, rest, _ := strings.Cut(c, "=")
		image, imageID, _ := strings.Cut(rest, "@@")
		p.Spec.Containers = append(p.Spec.Containers, corev1.Container{Name: name, Image: image})
		p.Status.ContainerStatuses = append(p.Status.ContainerStatuses, corev1.ContainerStatus{
			Name: name, Image: image, ImageID: imageID,
			State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}},
		})
	}
	return p
}

func int32p(n int32) *int32 { return &n }

func fixtures() []runtime.Object {
	app14 := "ghcr.io/acme/payments-api:1.4.2"
	app15 := "ghcr.io/acme/payments-api:1.5.0"
	envoy := "envoyproxy/envoy:v1.31.2"

	pending := runningPod("payments", "pending", "ReplicaSet", "rs-new", "app="+app15)
	pending.Status.Phase = corev1.PodPending

	return []runtime.Object{
		// A Deployment in the middle of a rollout from 1.4.2 to 1.5.0, with an envoy sidecar.
		&appsv1.Deployment{
			ObjectMeta: meta("payments", "payments-api", "dep-1", nil),
			Spec:       appsv1.DeploymentSpec{Replicas: int32p(3), Template: corev1.PodTemplateSpec{Spec: podSpec("app="+app15, "envoy="+envoy)}},
		},
		&appsv1.ReplicaSet{ObjectMeta: meta("payments", "payments-api-old", "rs-old", ownedBy("Deployment", "dep-1"))},
		&appsv1.ReplicaSet{ObjectMeta: meta("payments", "payments-api-new", "rs-new", ownedBy("Deployment", "dep-1"))},
		runningPod("payments", "p1", "ReplicaSet", "rs-old", "app="+app14+"@@docker-pullable://ghcr.io/acme/payments-api@"+digestA, "envoy="+envoy+"@@"+envoy+"@"+digestE),
		runningPod("payments", "p2", "ReplicaSet", "rs-old", "app="+app14+"@@ghcr.io/acme/payments-api@"+digestA, "envoy="+envoy+"@@"+envoy+"@"+digestE),
		runningPod("payments", "p3", "ReplicaSet", "rs-new", "app="+app15+"@@ghcr.io/acme/payments-api@"+digestB, "envoy="+envoy+"@@"+envoy+"@"+digestE),
		pending,

		// A StatefulSet whose runtime reports only the local image ID (no registry digest).
		&appsv1.StatefulSet{
			ObjectMeta: meta("data", "postgres", "sts-1", nil),
			Spec:       appsv1.StatefulSetSpec{Replicas: int32p(1), Template: corev1.PodTemplateSpec{Spec: podSpec("postgres=postgres:15.6")}},
		},
		runningPod("data", "postgres-0", "StatefulSet", "sts-1", "postgres=postgres:15.6@@sha256:"+strings.Repeat("c", 64)),

		// A DaemonSet.
		&appsv1.DaemonSet{
			ObjectMeta: meta("monitoring", "node-exporter", "ds-1", nil),
			Spec:       appsv1.DaemonSetSpec{Template: corev1.PodTemplateSpec{Spec: podSpec("exporter=quay.io/prometheus/node-exporter:v1.8.2")}},
			Status:     appsv1.DaemonSetStatus{DesiredNumberScheduled: 2},
		},
		runningPod("monitoring", "ne-a", "DaemonSet", "ds-1", "exporter=quay.io/prometheus/node-exporter:v1.8.2@@x@"+digestA),
		runningPod("monitoring", "ne-b", "DaemonSet", "ds-1", "exporter=quay.io/prometheus/node-exporter:v1.8.2@@x@"+digestA),

		// An idle CronJob and a running one (via its Job).
		&batchv1.CronJob{
			ObjectMeta: meta("payments", "nightly-report", "cj-1", nil),
			Spec:       batchv1.CronJobSpec{JobTemplate: batchv1.JobTemplateSpec{Spec: batchv1.JobSpec{Template: corev1.PodTemplateSpec{Spec: podSpec("report=ghcr.io/acme/report:2.0.1")}}}},
		},
		&batchv1.CronJob{
			ObjectMeta: meta("payments", "cleanup", "cj-2", nil),
			Spec:       batchv1.CronJobSpec{JobTemplate: batchv1.JobTemplateSpec{Spec: batchv1.JobSpec{Template: corev1.PodTemplateSpec{Spec: podSpec("cleanup=ghcr.io/acme/cleanup:0.3.0")}}}},
		},
		&batchv1.Job{ObjectMeta: meta("payments", "cleanup-123", "job-1", ownedBy("CronJob", "cj-2"))},
		runningPod("payments", "cleanup-123-x", "Job", "job-1", "cleanup=ghcr.io/acme/cleanup:0.3.0@@ghcr.io/acme/cleanup@"+digestB),

		// Scaled to zero.
		&appsv1.Deployment{
			ObjectMeta: meta("payments", "legacy", "dep-2", nil),
			Spec:       appsv1.DeploymentSpec{Replicas: int32p(0), Template: corev1.PodTemplateSpec{Spec: podSpec("app=ghcr.io/acme/legacy:0.9.0")}},
		},

		// Excluded namespace and a bare pod with no controller.
		&appsv1.Deployment{
			ObjectMeta: meta("kube-system", "coredns", "dep-3", nil),
			Spec:       appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{Spec: podSpec("coredns=registry.k8s.io/coredns/coredns:v1.11.1")}},
		},
		&corev1.Pod{ObjectMeta: meta("payments", "debug", "pod-debug", nil), Status: corev1.PodStatus{Phase: corev1.PodRunning}},
	}
}

// start runs the collector's watch and waits until it has synced.
func start(t *testing.T, c *Collector, changed func()) context.CancelFunc {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = c.Watch(ctx, changed)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	select {
	case <-c.synced:
	case <-time.After(10 * time.Second):
		t.Fatal("caches did not sync")
	}
	return cancel
}

func collect(t *testing.T, c *Collector) collectors.Result {
	t.Helper()
	res, err := c.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func byName(ws []agentproto.Workload) map[string]agentproto.Workload {
	m := map[string]agentproto.Workload{}
	for _, w := range ws {
		m[*w.Namespace+"/"+w.Name] = w
	}
	return m
}

func describe(cs []agentproto.Container) string {
	var parts []string
	for _, c := range cs {
		d := "-"
		if c.Digest != nil {
			d = (*c.Digest)[7:8]
		}
		parts = append(parts, c.Name+"="+c.Image+"@"+d+"x"+strconv.Itoa(c.Running))
	}
	return strings.Join(parts, " ")
}

func TestCollect(t *testing.T) {
	cs := fake.NewClientset(fixtures()...)
	c := newCollector(cs, agentproto.KubernetesSettings{ExcludeNamespaces: []string{"kube-system"}})
	start(t, c, func() {})

	res := collect(t, c)
	if !res.Complete || len(res.Errors) != 0 {
		t.Fatalf("complete=%v errors=%v", res.Complete, res.Errors)
	}
	got := byName(res.Workloads)
	if len(got) != 6 {
		t.Fatalf("workloads = %v", got)
	}
	if _, ok := got["kube-system/coredns"]; ok {
		t.Fatal("excluded namespace collected")
	}

	cases := map[string]struct {
		kind       agentproto.WorkloadKind
		desired    int
		containers string
	}{
		// Rollout: two app versions side by side, the pending pod not counted, the sidecar grouped.
		"payments/payments-api": {
			agentproto.Deployment, 3,
			"app=ghcr.io/acme/payments-api:1.4.2@ax2 app=ghcr.io/acme/payments-api:1.5.0@bx1 envoy=envoyproxy/envoy:v1.31.2@ex3",
		},
		// Bare sha256 image IDs are not registry digests.
		"data/postgres":            {agentproto.Statefulset, 1, "postgres=postgres:15.6@-x1"},
		"monitoring/node-exporter": {agentproto.Daemonset, 2, "exporter=quay.io/prometheus/node-exporter:v1.8.2@ax2"},
		"payments/nightly-report":  {agentproto.Cronjob, -1, "report=ghcr.io/acme/report:2.0.1@-x0"},
		"payments/cleanup":         {agentproto.Cronjob, -1, "cleanup=ghcr.io/acme/cleanup:0.3.0@bx1"},
		"payments/legacy":          {agentproto.Deployment, 0, "app=ghcr.io/acme/legacy:0.9.0@-x0"},
	}
	for name, want := range cases {
		w, ok := got[name]
		if !ok {
			t.Errorf("%s missing", name)
			continue
		}
		if w.Kind != want.kind {
			t.Errorf("%s kind = %s", name, w.Kind)
		}
		if want.desired >= 0 && (w.DesiredReplicas == nil || *w.DesiredReplicas != want.desired) {
			t.Errorf("%s desired = %v", name, w.DesiredReplicas)
		}
		if want.desired < 0 && w.DesiredReplicas != nil {
			t.Errorf("%s desired should be unset", name)
		}
		if d := describe(w.Containers); d != want.containers {
			t.Errorf("%s containers\n got: %s\nwant: %s", name, d, want.containers)
		}
		if w.Labels["app"] == "" || w.ID == "" {
			t.Errorf("%s labels/id not set: %+v", name, w)
		}
	}
}

func TestIncludeNamespaces(t *testing.T) {
	c := newCollector(fake.NewClientset(fixtures()...), agentproto.KubernetesSettings{IncludeNamespaces: []string{"data"}})
	start(t, c, func() {})
	res := collect(t, c)
	if len(res.Workloads) != 1 || res.Workloads[0].Name != "postgres" {
		t.Fatalf("workloads = %+v", res.Workloads)
	}
}

func TestWatchTriggersOnRelevantChanges(t *testing.T) {
	cs := fake.NewClientset(fixtures()...)
	c := newCollector(cs, agentproto.KubernetesSettings{})
	var changes atomic.Int32
	start(t, c, func() { changes.Add(1) })

	// Objects from the initial list do not count as changes.
	settle := func() int32 {
		prev := int32(-1)
		for prev != changes.Load() {
			prev = changes.Load()
			time.Sleep(100 * time.Millisecond)
		}
		return prev
	}
	base := settle()
	if base != 0 {
		t.Fatalf("initial list triggered %d changes", base)
	}
	ctx := context.Background()

	// A readiness-only pod update is noise.
	p, err := cs.CoreV1().Pods("payments").Get(ctx, "p3", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	p.Status.Conditions = append(p.Status.Conditions, corev1.PodCondition{Type: corev1.PodReady, Status: corev1.ConditionTrue})
	if _, err := cs.CoreV1().Pods("payments").UpdateStatus(ctx, p, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if n := settle(); n != base {
		t.Fatalf("readiness change triggered a collect (%d -> %d)", base, n)
	}

	// A new pod is a change.
	if _, err := cs.CoreV1().Pods("payments").Create(ctx,
		runningPod("payments", "p4", "ReplicaSet", "rs-new", "app=ghcr.io/acme/payments-api:1.5.0@@x@"+digestB),
		metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if n := settle(); n == base {
		t.Fatal("new pod did not trigger a collect")
	}
	res := collect(t, c)
	if d := describe(byName(res.Workloads)["payments/payments-api"].Containers); !strings.Contains(d, "1.5.0@bx2") {
		t.Fatalf("new pod not counted: %s", d)
	}
}

func TestCollectBeforeSyncTimesOutWithContext(t *testing.T) {
	c := newCollector(fake.NewClientset(), agentproto.KubernetesSettings{})
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := c.Collect(ctx); err == nil {
		t.Fatal("collect without a running watch should not succeed")
	}
}

func TestDigest(t *testing.T) {
	for in, want := range map[string]string{
		"docker-pullable://ghcr.io/a/b@" + digestA: digestA,
		"ghcr.io/a/b@" + digestB:                   digestB,
		"sha256:" + strings.Repeat("c", 64):        "",
		"ghcr.io/a/b@sha256:short":                 "",
		"":                                         "",
	} {
		if got := Digest(in); got != want {
			t.Errorf("Digest(%q) = %q, want %q", in, got, want)
		}
	}
}
