// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package ui

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/pipozzz/goliash/internal/store"
	"github.com/pipozzz/goliash/pkg/agentproto"
)

func TestConnectSettings(t *testing.T) {
	for _, c := range []struct {
		platform string
		form     url.Values
		want     string // a fragment of the JSON
		problem  string
	}{
		{"kubernetes", url.Values{"include": {"shop, pay"}, "exclude": {"kube-system"}}, `"include_namespaces":["shop","pay"]`, ""},
		{"docker", url.Values{"docker_host": {"tcp://socket-proxy:2375"}, "projects": {"shop"}}, `"projects":["shop"]`, ""},
		{"docker", url.Values{}, "", "Docker API"},
		{"swarm", url.Values{"docker_host": {"tcp://p:2375"}}, `"swarm":{"docker_host":"tcp://p:2375"}`, ""},
		{"nomad", url.Values{"address": {"http://nomad:4646"}, "acl": {"on"}, "region": {"eu"}}, `"credentials_ref":"nomad"`, ""},
		{"nomad", url.Values{"address": {"nomad:4646"}}, "", "Nomad address"},
		{"ecs", url.Values{"region": {"eu-west-1"}, "clusters": {"a\nb"}}, `"clusters":["a","b"]`, ""},
		{"ecs", url.Values{}, "", "AWS region"},
		{"compose", url.Values{"files": {"https://x/a.yml\nhttps://x/b.yml"}, "private": {"on"}}, `"credentials_ref":"compose"`, ""},
		{"compose", url.Values{"files": {" "}}, "", "Compose file"},
	} {
		got, problem := connectSettings(c.platform, c.form)
		if c.problem != "" {
			if !strings.Contains(problem, c.problem) {
				t.Errorf("%s %v: problem %q", c.platform, c.form, problem)
			}
			continue
		}
		var pt agentproto.Target
		if problem != "" || json.Unmarshal(got, &pt) != nil || !strings.Contains(string(got), c.want) {
			t.Errorf("%s: %s %q", c.platform, got, problem)
		}
	}
}

func TestInstallCommand(t *testing.T) {
	for _, p := range []string{"kubernetes", "docker", "swarm", "nomad", "ecs", "compose"} {
		title, cmd := installCommand(p, "https://g.example.com", "glsh_agent_x", nil, "1.13.1")
		if title == "" || !strings.Contains(cmd, "glsh_agent_x") || !strings.Contains(cmd, "https://g.example.com") {
			t.Errorf("%s: %s", p, cmd)
		}
	}
	_, cmd := installCommand("compose", "https://g", "t", []string{"/srv/shop/compose.yml", "/srv/shop/prod.yml", "https://x/y.yml"}, "1.13.1")
	if !strings.Contains(cmd, "-v /srv/shop:/srv/shop:ro -e GOLIASH_COMPOSE_DIRS=/srv/shop ") {
		t.Errorf("compose mounts: %s", cmd)
	}
}

func TestConnectFlow(t *testing.T) {
	e := newUIEnv(t)
	ctx := context.Background()
	admin := e.as(store.RoleAdmin)

	if _, body := get(t, admin, e.srv.URL+"/connect", nil); !strings.Contains(body, `href="/connect/kubernetes"`) || !strings.Contains(body, "Compose files") {
		t.Fatal("platform picker")
	}
	if _, body := get(t, e.as(store.RoleViewer), e.srv.URL+"/connect", nil); !strings.Contains(body, "does not allow") {
		t.Fatal("viewer may connect")
	}

	// A mistake keeps what was typed.
	_, body, _ := post(t, admin, e.srv.URL+"/connect/nomad", url.Values{"name": {"eu-nomad"}, "new_env": {"staging"}, "by": {"new"}, "address": {"nope"}})
	if !strings.Contains(body, "Enter the Nomad address") || !strings.Contains(body, `value="eu-nomad"`) {
		t.Fatalf("validation: %s", body)
	}
	if envs, _ := e.st.ListEnvironments(ctx, e.ws.Scope()); len(envs) != 1 {
		t.Fatal("environment created before the form was valid")
	}

	// New environment, new agent, target: one form.
	_, body, hdr := post(t, admin, e.srv.URL+"/connect/nomad", url.Values{
		"name": {"eu-nomad"}, "new_env": {"staging"}, "by": {"new"}, "agent_name": {"nomad-eu"},
		"address": {"http://nomad.service.consul:4646"}, "acl": {"on"},
	})
	if !strings.Contains(body, "Run the agent job in Nomad") || !strings.Contains(body, "glsh_agent_") || !strings.Contains(body, "Waiting for agent nomad-eu to connect") || hdr.Get("Cache-Control") != "no-store" {
		t.Fatalf("result: %s", body)
	}
	envs, _ := e.st.ListEnvironments(ctx, e.ws.Scope())
	if len(envs) != 2 || envs[1].Name != "staging" || envs[1].Position <= envs[0].Position {
		t.Fatalf("environments %+v", envs)
	}
	a, err := e.st.GetAgentByName(ctx, e.ws.Scope(), "nomad-eu")
	if err != nil {
		t.Fatal(err)
	}
	targets, _ := e.st.ListAgentTargets(ctx, e.ws.Scope(), a.ID)
	if len(targets) != 1 || !strings.Contains(string(targets[0].Settings), `"credentials_ref":"nomad"`) {
		t.Fatalf("target %+v", targets)
	}

	// The status polls itself until the first report.
	_, body = get(t, admin, e.srv.URL+"/connect/status/"+targets[0].ID, nil)
	if !strings.Contains(body, `hx-trigger="every 3s"`) {
		t.Fatalf("status: %s", body)
	}
	_, body = get(t, admin, e.srv.URL+"/connect/status/"+e.tgt.ID, nil) // reported already
	if strings.Contains(body, "every 3s") || !strings.Contains(body, "First report") {
		t.Fatalf("finished status: %s", body)
	}

	// An existing agent and the server need no install command.
	_, body, _ = post(t, admin, e.srv.URL+"/connect/compose", url.Values{"name": {"shop"}, "env": {e.prod.ID}, "by": {"server"}, "files": {"https://x/compose.yml"}})
	if !strings.Contains(body, "collected by this server") || strings.Contains(body, "glsh_agent_") {
		t.Fatalf("server: %s", body)
	}
	if code, _, _ := post(t, admin, e.srv.URL+"/connect/compose", url.Values{"name": {"shop"}, "env": {e.prod.ID}, "by": {"server"}, "files": {"https://x/c.yml"}}); code != http.StatusOK {
		t.Fatal("duplicate name")
	}
}

func TestInstallCommandPinsRelease(t *testing.T) {
	want := map[string][]string{
		"kubernetes": {"--version 1.13.1"},
		"docker":     {"GOLIASH_AGENT_VERSION=1.13.1", "/v1.13.1/"},
		"swarm":      {"GOLIASH_AGENT_VERSION=1.13.1", "/v1.13.1/"},
		"nomad":      {"/v1.13.1/", "goliash-agent:1.13.1"},
		"ecs":        {"?ref=v1.13.1", "goliash_token_arn=\"$ARN\""},
		"compose":    {"goliash-agent:1.13.1"},
	}
	for p, parts := range want {
		_, cmd := installCommand(p, "https://g", "t", []string{"/srv/a/compose.yml"}, "v1.13.1")
		for _, s := range parts {
			if !strings.Contains(cmd, s) {
				t.Errorf("%s lacks %q:\n%s", p, s, cmd)
			}
		}
		if strings.Contains(cmd, "/main/") || strings.Contains(cmd, "REPLACE") {
			t.Errorf("%s not pinned or has a placeholder:\n%s", p, cmd)
		}
	}
	if tag, ref := agentRelease("dev"); tag != "latest" || ref != "main" {
		t.Errorf("dev build: %s %s", tag, ref)
	}
}
