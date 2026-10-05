// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: Apache-2.0

package kubernetes

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/informers"
	k8sclient "k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/pipozzz/goliash/internal/collectors"
	"github.com/pipozzz/goliash/pkg/agentproto"
)

// syncTimeout bounds how long Collect waits for the informer caches to fill.
const syncTimeout = 2 * time.Minute

// New is the collectors.Factory for Kubernetes targets. It uses in-cluster
// credentials when running in a pod, otherwise the local kubeconfig (KUBECONFIG
// or ~/.kube/config), with the context named in the target settings if any.
func New(_ context.Context, t agentproto.Target) (collectors.Collector, error) {
	var settings agentproto.KubernetesSettings
	if t.Kubernetes != nil {
		settings = *t.Kubernetes
	}
	cfg, err := restConfig(settings)
	if err != nil {
		return nil, err
	}
	cfg.UserAgent = "goliash-agent"
	cs, err := k8sclient.NewForConfig(cfg)
	if err != nil {
		return nil, err
	}
	c := newCollector(cs, settings)
	c.readSecrets = ReadPullSecrets()
	return c, nil
}

func restConfig(s agentproto.KubernetesSettings) (*rest.Config, error) {
	if s.KubeconfigContext == nil || *s.KubeconfigContext == "" {
		if cfg, err := rest.InClusterConfig(); err == nil {
			return cfg, nil
		} else if !errors.Is(err, rest.ErrNotInCluster) {
			return nil, err
		}
	}
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	overrides := &clientcmd.ConfigOverrides{}
	if s.KubeconfigContext != nil {
		overrides.CurrentContext = *s.KubeconfigContext
	}
	cfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, overrides).ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("kubeconfig: %w", err)
	}
	return cfg, nil
}

// Collector reads Deployments, StatefulSets, DaemonSets and CronJobs and the images
// their running pods actually use. It keeps informer caches (list + watch), so it
// needs only get, list and watch permissions.
type Collector struct {
	cs       k8sclient.Interface
	factory  informers.SharedInformerFactory
	include  map[string]bool
	exclude  map[string]bool
	informer map[string]cache.SharedIndexInformer

	mu        sync.Mutex
	started   bool
	synced    chan struct{}
	watchErrs map[string]watchErr

	// Image pull secrets, read only when GOLIASH_READ_PULL_SECRETS allows it.
	readSecrets bool
	secrets     pullSecretCache
}

// watchErr is the latest list/watch failure of one resource. The reflector retries
// within seconds, so an error not repeated for errorTTL is treated as resolved.
type watchErr struct {
	msg string
	at  time.Time
}

const errorTTL = 2 * time.Minute

var _ collectors.Watcher = (*Collector)(nil)

func newCollector(cs k8sclient.Interface, s agentproto.KubernetesSettings) *Collector {
	f := informers.NewSharedInformerFactory(cs, 0)
	c := &Collector{
		cs:      cs,
		factory: f,
		include: set(s.IncludeNamespaces),
		exclude: set(s.ExcludeNamespaces),
		informer: map[string]cache.SharedIndexInformer{
			"deployments":  f.Apps().V1().Deployments().Informer(),
			"replicasets":  f.Apps().V1().ReplicaSets().Informer(),
			"statefulsets": f.Apps().V1().StatefulSets().Informer(),
			"daemonsets":   f.Apps().V1().DaemonSets().Informer(),
			"cronjobs":     f.Batch().V1().CronJobs().Informer(),
			"jobs":         f.Batch().V1().Jobs().Informer(),
			"pods":         f.Core().V1().Pods().Informer(),
		},
		synced:    make(chan struct{}),
		watchErrs: map[string]watchErr{},
	}
	for name, inf := range c.informer {
		_ = inf.SetWatchErrorHandler(func(_ *cache.Reflector, err error) {
			c.mu.Lock()
			c.watchErrs[name] = watchErr{msg: err.Error(), at: time.Now()}
			c.mu.Unlock()
		})
	}
	return c
}

func set(items []string) map[string]bool {
	m := make(map[string]bool, len(items))
	for _, i := range items {
		m[i] = true
	}
	return m
}

// Watch starts the informers and calls changed whenever a workload changes or a
// pod starts, stops or switches image. It blocks until ctx ends.
func (c *Collector) Watch(ctx context.Context, changed func()) error {
	c.mu.Lock()
	if c.started {
		c.mu.Unlock()
		return errors.New("already watching")
	}
	c.started = true
	c.mu.Unlock()

	handler := cache.ResourceEventHandlerDetailedFuncs{
		AddFunc: func(_ any, isInInitialList bool) {
			if !isInInitialList { // the first collect already covers the initial list
				changed()
			}
		},
		DeleteFunc: func(any) { changed() },
		UpdateFunc: func(oldObj, newObj any) {
			if relevantUpdate(oldObj, newObj) {
				changed()
			}
		},
	}
	for name, inf := range c.informer {
		if name == "replicasets" || name == "jobs" {
			continue // only used to resolve pod owners
		}
		if _, err := inf.AddEventHandler(handler); err != nil {
			return err
		}
	}

	c.factory.Start(ctx.Done())
	go func() {
		for _, ok := range c.factory.WaitForCacheSync(ctx.Done()) {
			if !ok {
				return
			}
		}
		close(c.synced)
	}()
	<-ctx.Done()
	c.factory.Shutdown()
	return nil
}

// relevantUpdate filters out the steady stream of status updates that do not
// change what is running (probe results, conditions, resync).
func relevantUpdate(oldObj, newObj any) bool {
	switch n := newObj.(type) {
	case *corev1.Pod:
		o, ok := oldObj.(*corev1.Pod)
		return !ok || o.Status.Phase != n.Status.Phase || podImages(o) != podImages(n)
	case metav1.Object:
		o, ok := oldObj.(metav1.Object)
		return !ok || o.GetGeneration() != n.GetGeneration() || !mapsEqual(o.GetLabels(), n.GetLabels())
	}
	return true
}

func podImages(p *corev1.Pod) string {
	var b strings.Builder
	for _, cs := range p.Status.ContainerStatuses {
		b.WriteString(cs.Name + "=" + cs.Image + "@" + cs.ImageID + ";")
	}
	return b.String()
}

func mapsEqual(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

// Collect returns every workload in the selected namespaces with the images its
// running pods use. Before the first sync it waits for the caches.
func (c *Collector) Collect(ctx context.Context) (collectors.Result, error) {
	timer := time.NewTimer(syncTimeout)
	defer timer.Stop()
	select {
	case <-c.synced:
	case <-ctx.Done():
		return collectors.Result{}, ctx.Err()
	case <-timer.C:
		return collectors.Result{}, fmt.Errorf("kubernetes caches did not sync within %s: %s", syncTimeout, c.errorSummary())
	}

	f := c.factory
	deployments, err := f.Apps().V1().Deployments().Lister().List(labels.Everything())
	if err != nil {
		return collectors.Result{}, err
	}
	replicaSets, _ := f.Apps().V1().ReplicaSets().Lister().List(labels.Everything())
	statefulSets, _ := f.Apps().V1().StatefulSets().Lister().List(labels.Everything())
	daemonSets, _ := f.Apps().V1().DaemonSets().Lister().List(labels.Everything())
	cronJobs, _ := f.Batch().V1().CronJobs().Lister().List(labels.Everything())
	jobs, _ := f.Batch().V1().Jobs().Lister().List(labels.Everything())
	pods, _ := f.Core().V1().Pods().Lister().List(labels.Everything())

	b := newBuilder()
	for _, d := range deployments {
		if c.selected(d.Namespace) {
			b.add(d.ObjectMeta, agentproto.Deployment, replicas(d.Spec.Replicas), d.Spec.Template.Spec)
		}
	}
	for _, s := range statefulSets {
		if c.selected(s.Namespace) {
			b.add(s.ObjectMeta, agentproto.Statefulset, replicas(s.Spec.Replicas), s.Spec.Template.Spec)
		}
	}
	for _, d := range daemonSets {
		if c.selected(d.Namespace) {
			desired := int(d.Status.DesiredNumberScheduled)
			b.add(d.ObjectMeta, agentproto.Daemonset, &desired, d.Spec.Template.Spec)
		}
	}
	for _, cj := range cronJobs {
		if c.selected(cj.Namespace) {
			b.add(cj.ObjectMeta, agentproto.Cronjob, nil, cj.Spec.JobTemplate.Spec.Template.Spec)
		}
	}

	owner := ownerIndex(replicaSets, jobs)
	for _, p := range pods {
		if !c.selected(p.Namespace) || p.Status.Phase != corev1.PodRunning {
			continue
		}
		if uid, ok := owner.workload(p); ok {
			b.addPod(uid, p)
		}
	}

	res := collectors.Result{Workloads: b.result(), Complete: true}
	if errs := c.currentErrors(); len(errs) > 0 {
		res.Complete, res.Errors = false, errs
	}
	return res, nil
}

func (c *Collector) selected(ns string) bool {
	if c.exclude[ns] {
		return false
	}
	return len(c.include) == 0 || c.include[ns]
}

// currentErrors lists recent list/watch failures, e.g. "cronjobs: forbidden".
func (c *Collector) currentErrors() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	names := make([]string, 0, len(c.watchErrs))
	for name, e := range c.watchErrs {
		if time.Since(e.at) > errorTTL {
			delete(c.watchErrs, name)
			continue
		}
		names = append(names, name)
	}
	slices.Sort(names)
	errs := make([]string, len(names))
	for i, name := range names {
		errs[i] = name + ": " + c.watchErrs[name].msg
	}
	return errs
}

func (c *Collector) errorSummary() string {
	if errs := c.currentErrors(); len(errs) > 0 {
		return strings.Join(errs, "; ")
	}
	return "no list/watch errors reported"
}

func replicas(r *int32) *int {
	n := 1 // Kubernetes default when unset
	if r != nil {
		n = int(*r)
	}
	return &n
}

// owners maps ReplicaSets to Deployments and Jobs to CronJobs, so pods resolve to
// the workload a person manages.
type owners struct {
	parent map[string]string // ReplicaSet or Job UID -> Deployment or CronJob UID
}

func ownerIndex(rss []*appsv1.ReplicaSet, jobs []*batchv1.Job) owners {
	o := owners{parent: map[string]string{}}
	for _, rs := range rss {
		if ref := controller(rs.OwnerReferences); ref != nil && ref.Kind == "Deployment" {
			o.parent[string(rs.UID)] = string(ref.UID)
		}
	}
	for _, j := range jobs {
		if ref := controller(j.OwnerReferences); ref != nil && ref.Kind == "CronJob" {
			o.parent[string(j.UID)] = string(ref.UID)
		}
	}
	return o
}

func (o owners) workload(p *corev1.Pod) (string, bool) {
	ref := controller(p.OwnerReferences)
	if ref == nil {
		return "", false
	}
	switch ref.Kind {
	case "StatefulSet", "DaemonSet":
		return string(ref.UID), true
	case "ReplicaSet", "Job":
		uid, ok := o.parent[string(ref.UID)]
		return uid, ok
	}
	return "", false
}

func controller(refs []metav1.OwnerReference) *metav1.OwnerReference {
	for i := range refs {
		if refs[i].Controller != nil && *refs[i].Controller {
			return &refs[i]
		}
	}
	return nil
}

// builder groups running containers per workload by name, image and digest.
type builder struct {
	order     []string
	workloads map[string]*agentproto.Workload
	templates map[string][]corev1.Container
	running   map[string]map[containerKey]int
}

type containerKey struct{ name, image, digest string }

func newBuilder() *builder {
	return &builder{
		workloads: map[string]*agentproto.Workload{},
		templates: map[string][]corev1.Container{},
		running:   map[string]map[containerKey]int{},
	}
}

func (b *builder) add(meta metav1.ObjectMeta, kind agentproto.WorkloadKind, desired *int, spec corev1.PodSpec) {
	uid := string(meta.UID)
	ns := meta.Namespace
	w := &agentproto.Workload{
		ID: uid, Kind: kind, Namespace: &ns, Name: meta.Name, DesiredReplicas: desired, Labels: meta.Labels,
	}
	b.order = append(b.order, uid)
	b.workloads[uid] = w
	b.templates[uid] = spec.Containers
	b.running[uid] = map[containerKey]int{}
}

func (b *builder) addPod(uid string, p *corev1.Pod) {
	counts, ok := b.running[uid]
	if !ok {
		return
	}
	statuses := map[string]corev1.ContainerStatus{}
	for _, cs := range p.Status.ContainerStatuses {
		statuses[cs.Name] = cs
	}
	for _, c := range p.Spec.Containers {
		k := containerKey{name: c.Name, image: c.Image}
		if cs, ok := statuses[c.Name]; ok {
			if cs.State.Running == nil {
				continue
			}
			k.digest = Digest(cs.ImageID)
		}
		counts[k]++
	}
}

func (b *builder) result() []agentproto.Workload {
	sort.SliceStable(b.order, func(i, j int) bool {
		wi, wj := b.workloads[b.order[i]], b.workloads[b.order[j]]
		if *wi.Namespace != *wj.Namespace {
			return *wi.Namespace < *wj.Namespace
		}
		if wi.Name != wj.Name {
			return wi.Name < wj.Name
		}
		return wi.Kind < wj.Kind
	})
	out := make([]agentproto.Workload, 0, len(b.order))
	for _, uid := range b.order {
		w := b.workloads[uid]
		counts := b.running[uid]
		seen := map[string]bool{}
		keys := make([]containerKey, 0, len(counts))
		for k := range counts {
			keys = append(keys, k)
		}
		sort.Slice(keys, func(i, j int) bool {
			if keys[i].name != keys[j].name {
				return keys[i].name < keys[j].name
			}
			if keys[i].image != keys[j].image {
				return keys[i].image < keys[j].image
			}
			return keys[i].digest < keys[j].digest
		})
		for _, k := range keys {
			c := agentproto.Container{Name: k.name, Image: k.image, Running: counts[k]}
			if k.digest != "" {
				d := k.digest
				c.Digest = &d
			}
			w.Containers = append(w.Containers, c)
			seen[k.name] = true
		}
		// Containers with nothing running (scaled to zero, idle CronJob) report the template image.
		for _, c := range b.templates[uid] {
			if !seen[c.Name] {
				w.Containers = append(w.Containers, agentproto.Container{Name: c.Name, Image: c.Image, Running: 0})
			}
		}
		if w.Containers == nil {
			w.Containers = []agentproto.Container{}
		}
		out = append(out, *w)
	}
	return out
}

// Digest extracts the manifest digest from a container status imageID such as
// "docker-pullable://ghcr.io/acme/app@sha256:…" or "ghcr.io/acme/app@sha256:…".
// A bare "sha256:…" is the local image config ID, not a registry digest, and is ignored.
func Digest(imageID string) string {
	_, d, ok := strings.Cut(imageID, "@")
	if !ok || !strings.HasPrefix(d, "sha256:") || len(d) != len("sha256:")+64 {
		return ""
	}
	return d
}
