// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: Apache-2.0

package swarm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/pipozzz/goliash/internal/collectors"
	"github.com/pipozzz/goliash/pkg/agentproto"
)

// New is the collectors.Factory for Docker Swarm targets. docker_host is
// tcp://host:port (normally a docker-socket-proxy that only allows GET) or
// unix:///path/to/docker.sock.
func New(_ context.Context, t agentproto.Target) (collectors.Collector, error) {
	if t.Swarm == nil || t.Swarm.DockerHost == "" {
		return nil, errors.New("swarm target has no docker_host")
	}
	base, hc, err := dockerClient(t.Swarm.DockerHost)
	if err != nil {
		return nil, err
	}
	return &Collector{base: base, http: hc}, nil
}

func dockerClient(host string) (string, *http.Client, error) {
	u, err := url.Parse(host)
	if err != nil {
		return "", nil, fmt.Errorf("docker_host: %w", err)
	}
	hc := &http.Client{Timeout: 30 * time.Second}
	switch u.Scheme {
	case "tcp", "http":
		return "http://" + u.Host, hc, nil
	case "https":
		return "https://" + u.Host, hc, nil
	case "unix":
		socket := u.Path
		hc.Transport = &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", socket)
		}}
		return "http://docker", hc, nil
	}
	return "", nil, fmt.Errorf("docker_host %q: use tcp://, https:// or unix://", host)
}

// Collector reads Swarm services and their running tasks with GET requests only.
type Collector struct {
	base string
	http *http.Client
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
	if err := c.get(ctx, "/services", &services); err != nil {
		return collectors.Result{}, err
	}
	var tasks []task
	filter := url.QueryEscape(`{"desired-state":["running"]}`)
	if err := c.get(ctx, "/tasks?filters="+filter, &tasks); err != nil {
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

func (c *Collector) get(ctx context.Context, path string, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+path, nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("docker API: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		var msg struct{ Message string }
		if json.Unmarshal(body, &msg) == nil && msg.Message != "" {
			return fmt.Errorf("docker API GET %s: %d %s", path, resp.StatusCode, msg.Message)
		}
		return fmt.Errorf("docker API GET %s: %d", path, resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(v)
}
