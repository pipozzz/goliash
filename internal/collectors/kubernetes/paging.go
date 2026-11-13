// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: Apache-2.0

package kubernetes

import (
	"context"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/tools/pager"
)

// eachItem lists in pages of pager.PageSize, so a large cluster is not read in one response, and calls fn
// for every item of type T. It returns the list error; fn sees no items then, or only part of them.
func eachItem[T any](ctx context.Context, list func(context.Context, metav1.ListOptions) (runtime.Object, error), fn func(T)) error {
	p := pager.New(pager.ListPageFunc(list))
	return p.EachListItem(ctx, metav1.ListOptions{}, func(obj runtime.Object) error {
		if item, ok := obj.(T); ok {
			fn(item)
		}
		return nil
	})
}

// eachResource pages through a custom resource in every namespace.
func (c *Collector) eachResource(ctx context.Context, gvr schema.GroupVersionResource, fn func(*unstructured.Unstructured)) error {
	return eachItem(ctx, func(ctx context.Context, opts metav1.ListOptions) (runtime.Object, error) {
		return c.dyn.Resource(gvr).Namespace(metav1.NamespaceAll).List(ctx, opts)
	}, fn)
}
