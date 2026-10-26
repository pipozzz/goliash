// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: Apache-2.0

package kubernetes

import (
	"slices"
	"testing"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/pipozzz/goliash/pkg/agentproto"
)

func service(ns, name string, typ corev1.ServiceType, selector map[string]string, ann map[string]string) *corev1.Service {
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name, Annotations: ann},
		Spec:       corev1.ServiceSpec{Type: typ, Selector: selector},
	}
}

// A workload is exposed through a public LoadBalancer service or an Ingress host its pods are behind; an internal
// load balancer, a plain ClusterIP service and a service marked internal do not count.
func TestExposure(t *testing.T) {
	objs := fixtures()
	objs = append(objs,
		service("payments", "api", corev1.ServiceTypeLoadBalancer, map[string]string{"app": "p1"}, nil),
		service("data", "db", corev1.ServiceTypeClusterIP, map[string]string{"app": "postgres-0"}, nil),
		service("monitoring", "metrics", corev1.ServiceTypeLoadBalancer, map[string]string{"app": "ne-a"},
			map[string]string{"service.beta.kubernetes.io/aws-load-balancer-scheme": "internal"}),
		service("payments", "hidden", corev1.ServiceTypeNodePort, map[string]string{"app": "cleanup-123-x"},
			map[string]string{"goliash.exposed": "internal"}),
		&networkingv1.Ingress{
			ObjectMeta: metav1.ObjectMeta{Namespace: "data", Name: "admin"},
			Spec: networkingv1.IngressSpec{Rules: []networkingv1.IngressRule{{
				Host: "db-admin.example.com",
				IngressRuleValue: networkingv1.IngressRuleValue{HTTP: &networkingv1.HTTPIngressRuleValue{Paths: []networkingv1.HTTPIngressPath{{
					Backend: networkingv1.IngressBackend{Service: &networkingv1.IngressServiceBackend{Name: "db"}},
				}}}},
			}}},
		},
	)
	objs = append(objs, service("payments", "web", corev1.ServiceTypeClusterIP, map[string]string{"app": "p3"}, nil))
	c := newCollector(fake.NewClientset(objs...), agentproto.KubernetesSettings{})
	c.dyn = fakeDynamic(&unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "gateway.networking.k8s.io/v1", "kind": "HTTPRoute",
		"metadata": map[string]any{"name": "shop", "namespace": "payments"},
		"spec": map[string]any{
			"hostnames": []any{"shop.example.com"},
			"rules":     []any{map[string]any{"backendRefs": []any{map[string]any{"name": "web", "port": int64(80)}}}},
		},
	}})
	start(t, c, func() {})
	got := byName(collect(t, c).Workloads)
	want := map[string][]string{
		"payments/payments-api":    {"HTTPRoute shop.example.com", "LoadBalancer service payments/api"},
		"data/postgres":            {"ingress db-admin.example.com"},
		"monitoring/node-exporter": nil,
		"payments/cleanup":         nil,
		"payments/legacy":          nil,
	}
	for name, w := range want {
		if !slices.Equal(got[name].Exposed, w) {
			t.Errorf("%s exposed %v, want %v", name, got[name].Exposed, w)
		}
	}
}
