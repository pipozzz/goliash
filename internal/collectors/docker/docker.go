// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: Apache-2.0

package docker

import (
	"context"
	"errors"
	"net/url"
	"slices"
	"sort"
	"strings"

	"github.com/pipozzz/goliash/internal/collectors"
	"github.com/pipozzz/goliash/pkg/agentproto"
)

// Docker and Compose labels the collector reads.
const (
	labelProject      = "com.docker.compose.project"
	labelService      = "com.docker.compose.service"
	labelOneoff       = "com.docker.compose.oneoff"
	labelSwarmService = "com.docker.swarm.service.id"
)

// New is the collectors.Factory for standalone Docker hosts.
func New(_ context.Context, t agentproto.Target) (collectors.Collector, error) {
	if t.Docker == nil || t.Docker.DockerHost == "" {
		return nil, errors.New("docker target has no docker_host")
	}
	c, err := NewClient(t.Docker.DockerHost)
	if err != nil {
		return nil, err
	}
	return &Collector{api: c, projects: t.Docker.Projects}, nil
}

// Collector reads the running containers of one Docker host.
type Collector struct {
	api      *Client
	projects []string // only these Compose projects; empty means all containers
}

type containerJSON struct {
	ID      string `json:"Id"`
	Names   []string
	Image   string
	ImageID string
	Labels  map[string]string
}

// Collect groups running containers into Compose services (by project and service)
// and single containers (by name). Swarm tasks and one-off `compose run` containers
// are skipped.
func (c *Collector) Collect(ctx context.Context) (collectors.Result, error) {
	var list []containerJSON
	if err := c.api.Get(ctx, "/containers/json", &list); err != nil {
		return collectors.Result{}, err
	}

	byID := map[string]*agentproto.Workload{}
	type group struct{ name, image, digest string }
	counts := map[string]map[group]int{}
	digests := map[string][]string{} // image ID -> repo digests, nil when unknown
	for _, ct := range list {
		if ct.Labels[labelSwarmService] != "" || strings.EqualFold(ct.Labels[labelOneoff], "true") {
			continue
		}
		if len(c.projects) > 0 && !slices.Contains(c.projects, ct.Labels[labelProject]) {
			continue
		}
		w := workloadFor(ct)
		if byID[w.ID] == nil {
			byID[w.ID] = &w
			counts[w.ID] = map[group]int{}
		}
		ref := c.imageRef(ctx, ct)
		image, digest := collectors.SplitImage(ref)
		if digest == "" && ct.ImageID != "" {
			if _, ok := digests[ct.ImageID]; !ok {
				digests[ct.ImageID] = c.repoDigests(ctx, ct.ImageID)
			}
			digest = matchDigest(image, digests[ct.ImageID])
		}
		counts[w.ID][group{w.Name, image, digest}]++
	}

	res := collectors.Result{Complete: true, Workloads: make([]agentproto.Workload, 0, len(byID))}
	for id, w := range byID {
		for g, n := range counts[id] {
			ct := agentproto.Container{Name: g.name, Image: g.image, Running: n}
			if g.digest != "" {
				ct.Digest = &g.digest
			}
			w.Containers = append(w.Containers, ct)
		}
		sort.Slice(w.Containers, func(i, j int) bool { return w.Containers[i].Image < w.Containers[j].Image })
		res.Workloads = append(res.Workloads, *w)
	}
	sort.Slice(res.Workloads, func(i, j int) bool { return res.Workloads[i].ID < res.Workloads[j].ID })
	return res, nil
}

// workloadFor names the workload a container belongs to. IDs are names, not container
// IDs, so a recreated container is the same workload.
func workloadFor(ct containerJSON) agentproto.Workload {
	labels := map[string]string{}
	for k, v := range ct.Labels {
		if !strings.HasPrefix(k, "com.docker.compose.") {
			labels[k] = v
		}
	}
	if project, service := ct.Labels[labelProject], ct.Labels[labelService]; project != "" && service != "" {
		return agentproto.Workload{
			ID: project + "/" + service, Kind: agentproto.ComposeService,
			Namespace: &project, Name: service, Labels: labels,
		}
	}
	name := ct.ID
	if len(ct.Names) > 0 {
		name = strings.TrimPrefix(ct.Names[0], "/")
	}
	return agentproto.Workload{ID: name, Kind: agentproto.DockerContainer, Name: name, Labels: labels}
}

// imageRef is the reference the container was created from. The list shows the image
// ID instead when the tag has since moved to another image; inspect has the original.
func (c *Collector) imageRef(ctx context.Context, ct containerJSON) string {
	if !strings.HasPrefix(ct.Image, "sha256:") {
		return ct.Image
	}
	var inspect struct{ Config struct{ Image string } }
	if err := c.api.Get(ctx, "/containers/"+url.PathEscape(ct.ID)+"/json", &inspect); err == nil && inspect.Config.Image != "" {
		return inspect.Config.Image
	}
	return ct.Image
}

// repoDigests lists the registry digests of a local image. It returns nil when the
// proxy does not allow image reads; the container is then reported without a digest.
func (c *Collector) repoDigests(ctx context.Context, imageID string) []string {
	var inspect struct{ RepoDigests []string }
	if err := c.api.Get(ctx, "/images/"+url.PathEscape(imageID)+"/json", &inspect); err != nil {
		return nil
	}
	return inspect.RepoDigests
}

// matchDigest picks the digest of the repository the container was started from;
// one local image can carry digests of several repositories.
func matchDigest(image string, repoDigests []string) string {
	repo := repository(image)
	for _, rd := range repoDigests {
		if name, digest, ok := strings.Cut(rd, "@"); ok && repository(name) == repo {
			return digest
		}
	}
	return ""
}

// repository strips the tag and the implicit docker.io/library/ prefix, so
// "nginx:1.27" and "docker.io/library/nginx" compare equal.
func repository(ref string) string {
	if i := strings.LastIndex(ref, ":"); i > strings.LastIndex(ref, "/") {
		ref = ref[:i]
	}
	ref = strings.TrimPrefix(ref, "docker.io/")
	return strings.TrimPrefix(ref, "library/")
}
