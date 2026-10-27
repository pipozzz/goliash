// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package ui

import (
	"context"
	"encoding/json"
	"net/url"
	"strings"
	"testing"

	"github.com/pipozzz/goliash/internal/store"
	"github.com/pipozzz/goliash/pkg/agentproto"
)

// An agent with a token reports what it found; what it does not collect yet is offered with one click, with hints
// for what it could not use, and the AWS page writes the policy and lines for it.
func TestFoundAndAddAWS(t *testing.T) {
	e := newUIEnv(t)
	ctx := context.Background()
	sc := e.ws.Scope()
	a, err := e.st.CreateAgent(ctx, sc, "dev-vpn-gw-1", "hash-found")
	if err != nil {
		t.Fatal(err)
	}
	envs, _ := e.st.ListEnvironments(ctx, sc)
	dockerSettings := json.RawMessage(`{"docker":{"docker_host":"tcp://socket-proxy:2375"}}`)
	if _, err := e.st.CreateTarget(ctx, store.Target{Scope: sc, EnvironmentID: envs[0].ID, AgentID: a.ID, Platform: "docker", Name: "gw-docker", Settings: dockerSettings}); err != nil {
		t.Fatal(err)
	}
	host, region := "tcp://socket-proxy:2375", "eu-central-1"
	found := []agentproto.DeclaredTarget{
		{Platform: agentproto.Docker, Name: "dev-vpn-gw-1", Docker: &agentproto.DockerSettings{DockerHost: host}},
		{Platform: agentproto.Ecs, Name: "prod", Ecs: &agentproto.ECSSettings{Region: region, Clusters: []string{"prod"}}},
		{Platform: agentproto.Rds, Name: "rds-eu-central-1", Rds: &agentproto.RDSSettings{Region: region}},
	}
	notes := []string{"msk eu-central-1: operation error Kafka: ListClustersV2, https response error StatusCode: 403, AccessDeniedException"}
	if err := e.st.SetAgentDiscovery(ctx, sc, a.ID, found, notes); err != nil {
		t.Fatal(err)
	}
	admin := e.as(store.RoleAdmin)
	if _, list := get(t, admin, e.srv.URL+"/agents", nil); !strings.Contains(list, ">2 found<") {
		t.Error("the agents list does not say what was found")
	}
	_, page := get(t, admin, e.srv.URL+"/agents/"+a.ID, nil)
	for _, want := range []string{"Found here, not watched", "<strong>ecs</strong> prod", "<strong>rds</strong> rds-eu-central-1", "may not read msk: allow kafka:ListClustersV2", "Add AWS to this agent"} {
		if !strings.Contains(page, want) {
			t.Errorf("agent page lacks %q", want)
		}
	}
	if strings.Contains(page, "<strong>docker</strong> dev-vpn-gw-1") {
		t.Error("the Docker host it already collects is offered again")
	}
	_, body, _ := post(t, admin, e.srv.URL+"/agents/"+a.ID+"/found", url.Values{"found": {"1", "2"}, "environment": {envs[len(envs)-1].Name}})
	if !strings.Contains(body, "Added prod, rds-eu-central-1") {
		t.Fatalf("add: %s", body[:min(len(body), 300)])
	}
	ts, _ := e.st.ListAgentTargets(ctx, sc, a.ID)
	if len(ts) != 3 || ts[1].Platform != "ecs" && ts[2].Platform != "ecs" {
		t.Errorf("targets %+v", ts)
	}
	if _, page := get(t, admin, e.srv.URL+"/agents/"+a.ID, nil); strings.Contains(page, "Found here, not watched") {
		t.Error("added targets are still offered")
	}

	_, aws := get(t, admin, e.srv.URL+"/agents/"+a.ID+"/aws?region=eu-west-1&service=rds&service=ecs", nil)
	for _, want := range []string{`rds:DescribeDBInstances`, `ecs:List*`, `AWS_REGION: &#34;eu-west-1&#34;`, "GOLIASH_LAMBDA: &#34;off&#34;", "docker compose -f goliash-agent.yml up -d", "aws iam create-user --user-name goliash-agent"} {
		if !strings.Contains(aws, want) {
			t.Errorf("AWS page lacks %q", want)
		}
	}
	if strings.Contains(aws, "kafka:ListClustersV2") {
		t.Error("the policy has a service not ticked")
	}

	// On EC2 the agent names its instance and role: one command, nothing to set on the agent.
	notes = append(notes, "aws ec2: instance i-0abc123 in eu-west-1, role dev-vpn-gw")
	if err := e.st.SetAgentDiscovery(ctx, sc, a.ID, found, notes); err != nil {
		t.Fatal(err)
	}
	if _, page := get(t, admin, e.srv.URL+"/agents/"+a.ID, nil); !strings.Contains(page, "It runs on EC2 with the role dev-vpn-gw") {
		t.Error("the agent page does not name the role")
	}
	_, aws = get(t, admin, e.srv.URL+"/agents/"+a.ID+"/aws", nil)
	for _, want := range []string{"One step", "i-0abc123", "aws iam put-role-policy --role-name dev-vpn-gw --policy-name goliash-read --policy-document &#39;{", "2. Nothing else", "docker restart"} {
		if !strings.Contains(aws, want) {
			t.Errorf("EC2 AWS page lacks %q", want)
		}
	}
	if strings.Contains(aws, "AWS_REGION") || strings.Contains(aws, "hop-limit") {
		t.Error("the EC2 AWS page asks for settings the agent finds itself")
	}

	// An instance without a role: make one and attach it.
	notes[len(notes)-1] = "aws ec2: instance i-0abc123 in eu-west-1 has no IAM role; give it one with the read-only policy from Add AWS"
	_ = e.st.SetAgentDiscovery(ctx, sc, a.ID, found, notes)
	if _, aws = get(t, admin, e.srv.URL+"/agents/"+a.ID+"/aws", nil); !strings.Contains(aws, "associate-iam-instance-profile --instance-id i-0abc123") {
		t.Error("no role: the page does not make one")
	}
}
