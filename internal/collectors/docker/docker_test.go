// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: Apache-2.0

package docker

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/pipozzz/goliash/pkg/agentproto"
)

var (
	d1 = "sha256:" + strings.Repeat("1", 64)
	d2 = "sha256:" + strings.Repeat("2", 64)
)

const containers = `[
 {"Id":"c1","Names":["/shop-web-1"],"Image":"nginx:1.27.2","ImageID":"img-nginx",
  "Labels":{"com.docker.compose.project":"shop","com.docker.compose.service":"web","goliash.service":"web"}},
 {"Id":"c2","Names":["/shop-web-2"],"Image":"nginx:1.27.2","ImageID":"img-nginx",
  "Labels":{"com.docker.compose.project":"shop","com.docker.compose.service":"web","goliash.service":"web"}},
 {"Id":"c3","Names":["/shop-worker-1"],"Image":"sha256:abc","ImageID":"img-worker",
  "Labels":{"com.docker.compose.project":"shop","com.docker.compose.service":"worker"}},
 {"Id":"c4","Names":["/portainer"],"Image":"portainer/portainer-ce:2.21.0@SHA2","ImageID":"img-portainer","Labels":{}},
 {"Id":"c5","Names":["/shop-web-run-1"],"Image":"nginx:1.27.2","ImageID":"img-nginx",
  "Labels":{"com.docker.compose.project":"shop","com.docker.compose.service":"web","com.docker.compose.oneoff":"True"}},
 {"Id":"c6","Names":["/api.1.xyz"],"Image":"ghcr.io/acme/api:1.0.0","ImageID":"img-api",
  "Labels":{"com.docker.swarm.service.id":"svc-api"}}
]`

func fakeDocker(t *testing.T, imagesAllowed bool) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("collector sent %s %s; only GET is allowed", r.Method, r.URL.Path)
		}
		body := ""
		switch {
		case r.URL.Path == "/containers/json":
			body = containers
		case r.URL.Path == "/containers/c3/json":
			body = `{"Config":{"Image":"ghcr.io/acme/worker:2.1.0"}}`
		case r.URL.Path == "/images/img-nginx/json" && imagesAllowed:
			body = `{"RepoDigests":["mirror.example/nginx@SHA2","nginx@SHA1"]}`
		case r.URL.Path == "/images/img-worker/json" && imagesAllowed:
			body = `{"RepoDigests":[]}`
		default:
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"message":"blocked by socket proxy"}`))
			return
		}
		_, _ = w.Write([]byte(strings.NewReplacer("SHA1", d1, "SHA2", d2).Replace(body)))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func collect(t *testing.T, imagesAllowed bool) map[string]agentproto.Workload {
	t.Helper()
	srv := fakeDocker(t, imagesAllowed)
	c, err := New(context.Background(), agentproto.Target{Docker: &agentproto.DockerSettings{DockerHost: srv.URL}})
	if err != nil {
		t.Fatal(err)
	}
	res, err := c.Collect(context.Background())
	if err != nil || !res.Complete {
		t.Fatalf("collect: %+v %v", res, err)
	}
	byID := map[string]agentproto.Workload{}
	for _, w := range res.Workloads {
		byID[w.ID] = w
	}
	return byID
}

func TestCollect(t *testing.T) {
	got := collect(t, true)
	if len(got) != 3 {
		t.Fatalf("want shop/web, shop/worker and portainer, got %v", got)
	}

	web := got["shop/web"]
	if web.Kind != agentproto.ComposeService || *web.Namespace != "shop" || web.Name != "web" || len(web.Containers) != 1 {
		t.Fatalf("compose service %+v", web)
	}
	if c := web.Containers[0]; c.Name != "web" || c.Image != "nginx:1.27.2" || c.Running != 2 || c.Digest == nil || *c.Digest != d1 {
		t.Fatalf("web container %+v (one-off run container must not count)", c)
	}
	if _, ok := web.Labels["com.docker.compose.project"]; ok || web.Labels["goliash.service"] != "web" {
		t.Fatalf("labels %v", web.Labels)
	}

	if c := got["shop/worker"].Containers[0]; c.Image != "ghcr.io/acme/worker:2.1.0" || c.Digest != nil {
		t.Fatalf("worker: image from inspect, no digest: %+v", c)
	}

	p := got["portainer"]
	if p.Kind != agentproto.DockerContainer || p.Namespace != nil || p.Containers[0].Image != "portainer/portainer-ce:2.21.0" || *p.Containers[0].Digest != d2 {
		t.Fatalf("standalone container %+v", p)
	}
}

// A proxy without IMAGES=1 still gives a complete result, just without digests.
func TestCollectWithoutImageAccess(t *testing.T) {
	if c := collect(t, false)["shop/web"].Containers[0]; c.Digest != nil || c.Running != 2 {
		t.Fatalf("web %+v", c)
	}
}

func TestRepository(t *testing.T) {
	for in, want := range map[string]string{
		"nginx:1.27":                     "nginx",
		"docker.io/library/nginx":        "nginx",
		"registry:5000/team/app:1.0":     "registry:5000/team/app",
		"registry:5000/team/app":         "registry:5000/team/app",
		"ghcr.io/acme/worker":            "ghcr.io/acme/worker",
		"docker.io/grafana/grafana:11.2": "grafana/grafana",
	} {
		if got := repository(in); got != want {
			t.Errorf("repository(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCollectOnlySomeProjects(t *testing.T) {
	srv := fakeDocker(t, true)
	c, _ := New(context.Background(), agentproto.Target{Docker: &agentproto.DockerSettings{DockerHost: srv.URL, Projects: []string{"shop"}}})
	res, err := c.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Workloads) != 2 || res.Workloads[0].ID != "shop/web" || res.Workloads[1].ID != "shop/worker" {
		t.Fatalf("want only the shop project, got %+v", res.Workloads)
	}
}
