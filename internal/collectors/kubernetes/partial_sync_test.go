// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: Apache-2.0

package kubernetes

import (
	"context"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/pipozzz/goliash/pkg/agentproto"
)

// A role that may not list cronjobs: what the agent may list is collected, the result marked incomplete, rather
// than every collect failing on caches that never sync.
func TestCollectWithoutCronJobs(t *testing.T) {
	old := syncTimeout
	syncTimeout = 2 * time.Second
	t.Cleanup(func() { syncTimeout = old })
	cs := fake.NewClientset(fixtures()...)
	cs.PrependReactor("list", "cronjobs", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Group: "batch", Resource: "cronjobs"}, "", nil)
	})
	c := newCollector(cs, agentproto.KubernetesSettings{})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = c.Watch(ctx, func() {}) }()
	t.Cleanup(func() { cancel(); <-done })
	res := collect(t, c)
	if res.Complete || len(res.Workloads) == 0 || !strings.Contains(strings.Join(res.Errors, " "), "cronjobs") {
		t.Fatalf("complete %v, %d workloads, errors %v", res.Complete, len(res.Workloads), res.Errors)
	}
	// The next collect does not wait for the caches again.
	began := time.Now()
	_ = collect(t, c)
	if time.Since(began) > time.Second {
		t.Errorf("the second collect waited %v", time.Since(began))
	}
}

// Cached objects keep what Goliash reads, not server-side apply's managed fields or kubectl's last applied copy.
func TestSlimCachedObjects(t *testing.T) {
	p := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
		Name: "web", Labels: map[string]string{"app": "web"},
		Annotations:   map[string]string{lastApplied: strings.Repeat("x", 10000), "goliash.service": "web"},
		ManagedFields: []metav1.ManagedFieldsEntry{{Manager: "kubectl"}},
	}}
	out, err := slim(p)
	got := out.(*corev1.Pod)
	if err != nil || got.ManagedFields != nil || got.Annotations[lastApplied] != "" || got.Annotations["goliash.service"] != "web" || got.Labels["app"] != "web" {
		t.Fatalf("%+v %v", got.ObjectMeta, err)
	}
}
