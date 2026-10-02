// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: Apache-2.0

package swarm

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

const services = `[
 {"ID":"svc-web","Spec":{"Name":"shop_web","Labels":{"com.docker.stack.namespace":"shop","goliash.service":"web"},
  "TaskTemplate":{"ContainerSpec":{"Image":"nginx:1.27.2@` + "SHA2" + `"}},"Mode":{"Replicated":{"Replicas":3}}}},
 {"ID":"svc-agent","Spec":{"Name":"node-agent","TaskTemplate":{"ContainerSpec":{"Image":"grafana/alloy:v1.4.0"}},"Mode":{"Global":{}}}},
 {"ID":"svc-idle","Spec":{"Name":"shop_worker","Labels":{"com.docker.stack.namespace":"shop"},
  "TaskTemplate":{"ContainerSpec":{"Image":"ghcr.io/acme/worker:2.1.0@` + "SHA1" + `"}},"Mode":{"Replicated":{"Replicas":0}}}}
]`

const tasks = `[
 {"ServiceID":"svc-web","Spec":{"ContainerSpec":{"Image":"nginx:1.27.1@SHA1"}},"Status":{"State":"running"}},
 {"ServiceID":"svc-web","Spec":{"ContainerSpec":{"Image":"nginx:1.27.2@SHA2"}},"Status":{"State":"running"}},
 {"ServiceID":"svc-web","Spec":{"ContainerSpec":{"Image":"nginx:1.27.2@SHA2"}},"Status":{"State":"running"}},
 {"ServiceID":"svc-web","Spec":{"ContainerSpec":{"Image":"nginx:1.27.2@SHA2"}},"Status":{"State":"preparing"}},
 {"ServiceID":"svc-agent","Spec":{"ContainerSpec":{"Image":"grafana/alloy:v1.4.0"}},"Status":{"State":"running"}},
 {"ServiceID":"svc-agent","Spec":{"ContainerSpec":{"Image":"grafana/alloy:v1.4.0"}},"Status":{"State":"running"}}
]`

func fakeDocker(t *testing.T) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("collector sent %s %s; only GET is allowed", r.Method, r.URL.Path)
		}
		body := ""
		switch r.URL.Path {
		case "/services":
			body = services
		case "/tasks":
			if !strings.Contains(r.URL.Query().Get("filters"), `"desired-state":["running"]`) {
				t.Errorf("tasks not filtered: %s", r.URL.RawQuery)
			}
			body = tasks
		default:
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"message":"blocked by socket proxy"}`))
			return
		}
		body = strings.NewReplacer("SHA1", d1, "SHA2", d2).Replace(body)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestCollect(t *testing.T) {
	srv := fakeDocker(t)
	c, err := New(context.Background(), agentproto.Target{Swarm: &agentproto.SwarmSettings{DockerHost: "tcp://" + strings.TrimPrefix(srv.URL, "http://")}})
	if err != nil {
		t.Fatal(err)
	}
	res, err := c.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !res.Complete || len(res.Workloads) != 3 {
		t.Fatalf("result %+v", res)
	}

	byName := map[string]agentproto.Workload{}
	for _, w := range res.Workloads {
		byName[w.Name] = w
	}

	web := byName["shop_web"]
	if web.Kind != agentproto.SwarmService || *web.Namespace != "shop" || *web.DesiredReplicas != 3 || web.Labels["goliash.service"] != "web" {
		t.Fatalf("web %+v", web)
	}
	if len(web.Containers) != 2 ||
		web.Containers[0].Image != "nginx:1.27.1" || *web.Containers[0].Digest != d1 || web.Containers[0].Running != 1 ||
		web.Containers[1].Image != "nginx:1.27.2" || *web.Containers[1].Digest != d2 || web.Containers[1].Running != 2 ||
		web.Containers[0].Name != "web" {
		t.Fatalf("web containers %+v", web.Containers)
	}

	agent := byName["node-agent"]
	if agent.DesiredReplicas != nil || agent.Namespace != nil || len(agent.Containers) != 1 ||
		agent.Containers[0].Running != 2 || agent.Containers[0].Digest != nil {
		t.Fatalf("global service %+v", agent)
	}

	idle := byName["shop_worker"]
	if len(idle.Containers) != 1 || idle.Containers[0].Running != 0 || idle.Containers[0].Image != "ghcr.io/acme/worker:2.1.0" || *idle.Containers[0].Digest != d1 {
		t.Fatalf("idle service %+v", idle.Containers)
	}
}

func TestDockerHost(t *testing.T) {
	for host, want := range map[string]string{
		"tcp://proxy:2375":            "http://proxy:2375",
		"https://docker.example:2376": "https://docker.example:2376",
		"unix:///var/run/docker.sock": "http://docker",
	} {
		base, _, err := dockerClient(host)
		if err != nil || base != want {
			t.Errorf("%s -> %s, %v", host, base, err)
		}
	}
	if _, _, err := dockerClient("ssh://host"); err == nil {
		t.Error("ssh accepted")
	}
	if _, err := New(context.Background(), agentproto.Target{}); err == nil {
		t.Error("missing settings accepted")
	}
}

func TestAPIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"message":"blocked by socket proxy"}`))
	}))
	defer srv.Close()
	c, _ := New(context.Background(), agentproto.Target{Swarm: &agentproto.SwarmSettings{DockerHost: srv.URL}})
	_, err := c.Collect(context.Background())
	if err == nil || !strings.Contains(err.Error(), "blocked by socket proxy") {
		t.Fatalf("err = %v", err)
	}
}
