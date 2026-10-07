// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: Apache-2.0

package discover

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
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
		AWS:      &fakeAWS{},
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

// fakeAWS answers for account 123456789012; regions without clusters or Lambda are
// denied.
type fakeAWS struct {
	clusters map[string][]string
	lambda   map[string]bool
	asked    []string
}

func (f *fakeAWS) Account(context.Context, string) (string, error) { return "123456789012", nil }

func (f *fakeAWS) ECSClusters(_ context.Context, region string) ([]string, error) {
	f.asked = append(f.asked, "ecs "+region)
	cs, ok := f.clusters[region]
	if !ok {
		return nil, errors.New("AccessDeniedException")
	}
	return cs, nil
}

func (f *fakeAWS) CanListFunctions(_ context.Context, region string) error {
	f.asked = append(f.asked, "lambda "+region)
	if !f.lambda[region] {
		return errors.New("AccessDeniedException")
	}
	return nil
}

func names(ts []agentproto.DeclaredTarget) string {
	var out []string
	for _, t := range ts {
		out = append(out, string(t.Platform)+" "+t.Name)
	}
	return strings.Join(out, ", ")
}

func TestAWS(t *testing.T) {
	arn := func(region, name string) string { return "arn:aws:ecs:" + region + ":123456789012:cluster/" + name }
	f := &fakeAWS{
		clusters: map[string][]string{
			"eu-west-1":    {arn("eu-west-1", "prod"), arn("eu-west-1", "staging"), arn("eu-west-1", "tools")},
			"eu-central-1": {arn("eu-central-1", "prod")},
		},
		lambda: map[string]bool{"eu-west-1": true},
	}
	o := opts(map[string]string{
		"AWS_REGION": "eu-west-1", "GOLIASH_AWS_REGIONS": "eu-central-1, us-east-1",
		"GOLIASH_LAMBDA_ALIASES": "live=prod, canary=-, broken",
	})
	o.AWS = f
	r, err := Run(context.Background(), o)
	if err != nil || r.Identity != "aws:123456789012/eu-west-1" || r.Name != "aws-eu-west-1" {
		t.Fatalf("aws: %+v %v", r, err)
	}
	// Every cluster its own target; another region's clusters carry the region.
	if got := names(r.Targets); got != "ecs prod, ecs staging, ecs tools, lambda lambda-eu-west-1, ecs prod-eu-central-1" {
		t.Fatalf("targets: %s", got)
	}
	if l := r.Targets[3].Lambda; len(l.AliasEnvironments) != 2 || l.AliasEnvironments["live"] != "prod" || l.AliasEnvironments["canary"] != "-" {
		t.Fatalf("aliases: %+v", l)
	}
	if c := r.Targets[4].Ecs; c.Region != "eu-central-1" || c.Clusters[0] != "prod" {
		t.Fatalf("other region: %+v", c)
	}
	if len(r.Notes) != 3 { // no Lambda in eu-central-1, nothing in us-east-1
		t.Fatalf("notes: %v", r.Notes)
	}

	// Only some clusters, and Lambda off.
	o = opts(map[string]string{"AWS_REGION": "eu-west-1", "GOLIASH_ECS_CLUSTERS": "prod,staging", "GOLIASH_LAMBDA": "off"})
	f.asked = nil
	o.AWS = f
	r, _ = Run(context.Background(), o)
	if got := names(r.Targets); got != "ecs prod, ecs staging" || slices.Contains(f.asked, "lambda eu-west-1") {
		t.Fatalf("filtered: %s %v", got, f.asked)
	}
}
