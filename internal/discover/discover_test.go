// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: Apache-2.0

package discover

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/pipozzz/goliash/pkg/agentproto"
)

func opts(env map[string]string) Options {
	return Options{
		Env:      func(k string) string { return env[k] },
		ReadFile: func(p string) ([]byte, error) { return nil, errors.New("no " + p) },
		Exists:   func(string) bool { return false },
		Hostname: func() (string, error) { return "box", nil },
		Lambda:   func(context.Context, string) (string, error) { return "", errors.New("AccessDeniedException") },
	}
}

func TestDocker(t *testing.T) {
	info := `{"ID":"ENGINE-1","Name":"web-01","Swarm":{"LocalNodeState":"inactive"}}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/info" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(info))
	}))
	defer srv.Close()
	host := "tcp://" + strings.TrimPrefix(srv.URL, "http://")

	r, err := Run(context.Background(), opts(map[string]string{"DOCKER_HOST": host}))
	if err != nil || r.Identity != "docker:ENGINE-1" || r.Name != "web-01" || len(r.Targets) != 1 ||
		r.Targets[0].Platform != agentproto.Docker || r.Targets[0].Docker.DockerHost != host {
		t.Fatalf("docker: %+v %v", r, err)
	}

	info = `{"ID":"ENGINE-1","Name":"mgr-1","Swarm":{"LocalNodeState":"active","ControlAvailable":true,"Cluster":{"ID":"SWARM-9"}}}`
	r, err = Run(context.Background(), opts(map[string]string{"DOCKER_HOST": host, "GOLIASH_AGENT_NAME": "prod"}))
	if err != nil || r.Identity != "swarm:SWARM-9" || r.Name != "prod" || r.Targets[0].Platform != agentproto.Swarm ||
		r.Targets[0].Name != "prod" {
		t.Fatalf("swarm: %+v %v", r, err)
	}
}

func TestECSAndKubernetes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"Cluster":"arn:aws:ecs:eu-west-1:123456789012:cluster/prod"}`))
	}))
	defer srv.Close()
	r, err := Run(context.Background(), opts(map[string]string{"ECS_CONTAINER_METADATA_URI_V4": srv.URL + "/v4/x"}))
	if err != nil || r.Identity != "ecs:arn:aws:ecs:eu-west-1:123456789012:cluster/prod" || r.Name != "prod" ||
		r.Targets[0].Ecs.Region != "eu-west-1" || r.Targets[0].Ecs.Clusters[0] != "prod" {
		t.Fatalf("ecs: %+v %v", r, err)
	}

	o := opts(map[string]string{"KUBERNETES_SERVICE_HOST": "10.0.0.1"})
	o.ReadFile = func(string) ([]byte, error) { return []byte("CA"), nil }
	r, err = Run(context.Background(), o)
	if err != nil || !strings.HasPrefix(r.Identity, "kubernetes:") || r.Targets[0].Platform != agentproto.Kubernetes {
		t.Fatalf("kubernetes: %+v %v", r, err)
	}
}

func TestNomadDeclaredAndOverrides(t *testing.T) {
	r, err := Run(context.Background(), opts(map[string]string{
		"NOMAD_ADDR": "http://10.0.0.5:4646", "NOMAD_REGION": "eu", "NOMAD_NAMESPACE": "default", "NOMAD_JOB_ID": "goliash-agent",
		"GOLIASH_CREDENTIAL_NOMAD": "secret",
		"GOLIASH_TARGETS":          `[{"platform":"compose","name":"shop","compose":{"files":["/srv/shop/compose.yml"]}}]`,
	}))
	if err != nil || r.Identity != "nomad:eu@default/goliash-agent" || r.Name != "nomad-eu" || len(r.Targets) != 2 ||
		*r.Targets[0].CredentialsRef != "nomad" || r.Targets[1].Compose.Files[0] != "/srv/shop/compose.yml" {
		t.Fatalf("nomad: %+v %v", r, err)
	}

	if _, err := Run(context.Background(), opts(map[string]string{"GOLIASH_TARGETS": `[{"platform":"x","name":"y"}]`})); err == nil {
		t.Fatal("bad declared target accepted")
	}
	if r, err := Run(context.Background(), opts(nil)); err != nil || r.Identity != "" || r.Name != "box" {
		t.Fatalf("nothing found: %+v %v", r, err)
	}
	r, err = Run(context.Background(), opts(map[string]string{
		"GOLIASH_AGENT_ID": "lab-1",
		"GOLIASH_TARGETS":  `[{"platform":"compose","name":"shop","compose":{"files":["https://x/y.yml"]}}]`,
	}))
	if err != nil || r.Identity != "lab-1" || r.Name != "box" {
		t.Fatalf("override: %+v %v", r, err)
	}
}

func TestLambda(t *testing.T) {
	o := opts(map[string]string{"AWS_REGION": "eu-central-1"})
	o.Lambda = func(_ context.Context, region string) (string, error) {
		if region != "eu-central-1" {
			return "", errors.New("wrong region")
		}
		return "123456789012", nil
	}
	r, err := Run(context.Background(), o)
	if err != nil || len(r.Targets) != 1 || r.Targets[0].Platform != agentproto.Lambda || r.Targets[0].Lambda.Region != "eu-central-1" ||
		r.Targets[0].Name != "lambda-eu-central-1" || r.Identity != "lambda:123456789012/eu-central-1" {
		t.Fatalf("lambda: %+v %v", r, err)
	}
	// Without the permission it is only a note; off skips the check.
	r, _ = Run(context.Background(), opts(map[string]string{"AWS_REGION": "eu-central-1"}))
	if len(r.Targets) != 0 || len(r.Notes) != 1 {
		t.Fatalf("denied: %+v", r)
	}
	o = opts(map[string]string{"AWS_REGION": "eu-central-1", "GOLIASH_LAMBDA": "off"})
	o.Lambda = func(context.Context, string) (string, error) { t.Fatal("checked although off"); return "", nil }
	_, _ = Run(context.Background(), o)
}
