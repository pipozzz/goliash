// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: Apache-2.0

package kubernetes

import (
	"context"
	"slices"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

var httpRoutes = schema.GroupVersionResource{Group: "gateway.networking.k8s.io", Version: "v1", Resource: "httproutes"}

// internalLB are the annotations that keep a LoadBalancer service on a private address, per cloud.
var internalLB = map[string][]string{
	"service.beta.kubernetes.io/aws-load-balancer-internal":          {"true", "0.0.0.0/0"},
	"service.beta.kubernetes.io/aws-load-balancer-scheme":            {"internal"},
	"networking.gke.io/load-balancer-type":                           {"Internal"},
	"cloud.google.com/load-balancer-type":                            {"Internal"},
	"service.beta.kubernetes.io/azure-load-balancer-internal":        {"true"},
	"service.beta.kubernetes.io/oci-load-balancer-internal":          {"true"},
	"service.beta.kubernetes.io/openstack-internal-load-balancer":    {"true"},
	"service.kubernetes.io/ibm-load-balancer-cloud-provider-ip-type": {"private"},
}

// exposure reads how each workload can be reached from outside the cluster: through an Ingress or HTTPRoute
// host, or a LoadBalancer or NodePort service. It maps workload UID to the ways, each once. Without permission
// to list services or ingresses it finds nothing, and the workloads still count.
func (c *Collector) exposure(ctx context.Context, pods []*corev1.Pod, owner owners) map[string][]string {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	svcs, err := c.cs.CoreV1().Services(metav1.NamespaceAll).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil
	}
	// Each service's ways in: its own type, and the routes that send to it.
	ways := map[string][]string{} // namespace/name -> ways
	for _, s := range svcs.Items {
		if s.Annotations["goliash.exposed"] == "internal" || s.Labels["goliash.exposed"] == "internal" {
			continue
		}
		key := s.Namespace + "/" + s.Name
		switch s.Spec.Type {
		case corev1.ServiceTypeLoadBalancer:
			if !internalLoadBalancer(s.Annotations) {
				ways[key] = append(ways[key], "LoadBalancer service "+key)
			}
		case corev1.ServiceTypeNodePort:
			ways[key] = append(ways[key], "NodePort service "+key)
		}
	}
	if ings, err := c.cs.NetworkingV1().Ingresses(metav1.NamespaceAll).List(ctx, metav1.ListOptions{}); err == nil {
		for _, ing := range ings.Items {
			for svc, host := range ingressBackends(ing) {
				key := ing.Namespace + "/" + svc
				ways[key] = append(ways[key], "ingress "+host)
			}
		}
	}
	if c.dyn != nil {
		if routes, err := c.dyn.Resource(httpRoutes).Namespace(metav1.NamespaceAll).List(ctx, metav1.ListOptions{}); err == nil {
			for _, r := range routes.Items {
				for svc, host := range routeBackends(r) {
					key := r.GetNamespace() + "/" + svc
					ways[key] = append(ways[key], "HTTPRoute "+host)
				}
			}
		}
	}

	out := map[string][]string{}
	for _, s := range svcs.Items {
		w := ways[s.Namespace+"/"+s.Name]
		if len(w) == 0 || len(s.Spec.Selector) == 0 {
			continue
		}
		sel := labels.SelectorFromSet(s.Spec.Selector)
		for _, p := range pods {
			if p.Namespace != s.Namespace || !sel.Matches(labels.Set(p.Labels)) {
				continue
			}
			if uid, ok := owner.workload(p); ok {
				for _, way := range w {
					if !slices.Contains(out[uid], way) {
						out[uid] = append(out[uid], way)
					}
				}
			}
		}
	}
	for uid := range out {
		slices.Sort(out[uid])
	}
	return out
}

func internalLoadBalancer(ann map[string]string) bool {
	for key, values := range internalLB {
		if v, ok := ann[key]; ok && slices.ContainsFunc(values, func(want string) bool { return strings.EqualFold(v, want) }) {
			return true
		}
	}
	return false
}

// ingressBackends maps each service an Ingress sends to, to the host it answers on (or the Ingress's name).
func ingressBackends(ing networkingv1.Ingress) map[string]string {
	out := map[string]string{}
	name := ing.Namespace + "/" + ing.Name
	if b := ing.Spec.DefaultBackend; b != nil && b.Service != nil {
		out[b.Service.Name] = name
	}
	for _, rule := range ing.Spec.Rules {
		host := rule.Host
		if host == "" {
			host = name
		}
		if rule.HTTP == nil {
			continue
		}
		for _, p := range rule.HTTP.Paths {
			if p.Backend.Service != nil {
				if _, seen := out[p.Backend.Service.Name]; !seen {
					out[p.Backend.Service.Name] = host
				}
			}
		}
	}
	return out
}

// routeBackends maps each service a Gateway API HTTPRoute sends to, to its first hostname (or the route's name).
func routeBackends(r unstructured.Unstructured) map[string]string {
	host := r.GetNamespace() + "/" + r.GetName()
	if hosts, _, _ := unstructured.NestedStringSlice(r.Object, "spec", "hostnames"); len(hosts) > 0 {
		host = hosts[0]
	}
	out := map[string]string{}
	rules, _, _ := unstructured.NestedSlice(r.Object, "spec", "rules")
	for _, rule := range rules {
		m, ok := rule.(map[string]any)
		if !ok {
			continue
		}
		refs, _, _ := unstructured.NestedSlice(m, "backendRefs")
		for _, ref := range refs {
			b, ok := ref.(map[string]any)
			if !ok {
				continue
			}
			if kind := str(b["kind"]); kind != "" && kind != "Service" {
				continue
			}
			if name := str(b["name"]); name != "" {
				out[name] = host
			}
		}
	}
	return out
}
