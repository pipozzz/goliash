// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: Apache-2.0

package swarm

import (
	"context"
	"errors"
	"net/url"
	"sort"
	"strings"

	"github.com/pipozzz/goliash/internal/collectors"
	"github.com/pipozzz/goliash/internal/collectors/docker"
	"github.com/pipozzz/goliash/pkg/agentproto"
)

// New is the collectors.Factory for Docker Swarm targets. docker_host is
// tcp://host:port (normally a docker-socket-proxy that only allows GET) or
// unix:///path/to/docker.sock.
func New(_ context.Context, t agentproto.Target) (collectors.Collector, error) {
	if t.Swarm == nil || t.Swarm.DockerHost == "" {
		return nil, errors.New("swarm target has no docker_host")
	}
	api, err := docker.NewClient(t.Swarm.DockerHost)
	if err != nil {
		return nil, err
	}
	return &Collector{api: api}, nil
}

// Collector reads Swarm services and their running tasks with GET requests only.
type Collector struct {
	api *docker.Client
}

type service struct {
	ID   string
	Spec struct {
		Name         string
		Labels       map[string]string
		TaskTemplate struct {
			ContainerSpec struct {
				Image string
			}
		}
		Mode struct {
			Replicated *struct{ Replicas *int }
			Global     *struct{}
		}
	}
}

type task struct {
	ServiceID string
	Spec      struct {
		ContainerSpec struct {
			Image string
		}
	}
	Status struct {
		State string
	}
}

// Collect lists services and counts running tasks per image.
func (c *Collector) Collect(ctx context.Context) (collectors.Result, error) {
	var services []service
	if err := c.api.Get(ctx, "/services", &services); err != nil {
		return collectors.Result{}, err
	}
	var tasks []task
	filter := url.QueryEscape(`{"desired-state":["running"]}`)
	if err := c.api.Get(ctx, "/tasks?filters="+filter, &tasks); err != nil {
		return collectors.Result{}, err
	}

	running := map[string]map[string]int{} // service ID -> image ref -> count
	for _, t := range tasks {
		if t.Status.State != "running" {
			continue
		}
		if running[t.ServiceID] == nil {
			running[t.ServiceID] = map[string]int{}
		}
		running[t.ServiceID][t.Spec.ContainerSpec.Image]++
	}

	sort.Slice(services, func(i, j int) bool { return services[i].Spec.Name < services[j].Spec.Name })
	res := collectors.Result{Complete: true, Workloads: make([]agentproto.Workload, 0, len(services))}
	for _, s := range services {
		w := agentproto.Workload{
			ID: s.ID, Kind: agentproto.SwarmService, Name: s.Spec.Name, Labels: s.Spec.Labels,
			Containers: []agentproto.Container{},
		}
		if stack := s.Spec.Labels["com.docker.stack.namespace"]; stack != "" {
			w.Namespace = &stack
		}
		if r := s.Spec.Mode.Replicated; r != nil && r.Replicas != nil {
			n := *r.Replicas
			w.DesiredReplicas = &n
		}

		refs := make([]string, 0, len(running[s.ID]))
		for ref := range running[s.ID] {
			refs = append(refs, ref)
		}
		sort.Strings(refs)
		for _, ref := range refs {
			w.Containers = append(w.Containers, container(s.Spec.Name, ref, running[s.ID][ref]))
		}
		if len(w.Containers) == 0 {
			w.Containers = append(w.Containers, container(s.Spec.Name, s.Spec.TaskTemplate.ContainerSpec.Image, 0))
		}
		res.Workloads = append(res.Workloads, w)
	}
	return res, nil
}

// container names the single container of a Swarm service after the service,
// without its stack prefix ("shop_web" -> "web").
func container(service, ref string, running int) agentproto.Container {
	name := service
	if _, after, ok := strings.Cut(service, "_"); ok && after != "" {
		name = after
	}
	image, digest := collectors.SplitImage(ref)
	c := agentproto.Container{Name: name, Image: image, Running: running}
	if digest != "" {
		c.Digest = &digest
	}
	return c
}
