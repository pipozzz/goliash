// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: Apache-2.0

package kubernetes

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// A large list comes in pages: every page is read, each asked for with the previous one's continue token.
func TestEachItemReadsEveryPage(t *testing.T) {
	pages := map[string]*corev1.ServiceList{
		"":   {ListMeta: metav1.ListMeta{Continue: "p2"}, Items: []corev1.Service{{ObjectMeta: metav1.ObjectMeta{Name: "a"}}}},
		"p2": {ListMeta: metav1.ListMeta{Continue: "p3"}, Items: []corev1.Service{{ObjectMeta: metav1.ObjectMeta{Name: "b"}}}},
		"p3": {Items: []corev1.Service{{ObjectMeta: metav1.ObjectMeta{Name: "c"}}}},
	}
	var limits []int64
	var names []string
	err := eachItem(context.Background(), func(_ context.Context, opts metav1.ListOptions) (runtime.Object, error) {
		limits = append(limits, opts.Limit)
		return pages[opts.Continue], nil
	}, func(s *corev1.Service) { names = append(names, s.Name) })
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 3 || names[0] != "a" || names[2] != "c" {
		t.Errorf("items = %v, want a b c", names)
	}
	for _, l := range limits {
		if l <= 0 {
			t.Errorf("a page was asked for without a limit: %v", limits)
		}
	}
}
