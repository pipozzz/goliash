// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package ui

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/pipozzz/goliash/internal/tokens"

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
		"nomad":      {"/v1.13.1/", "-var version=1.13.1"},
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

func TestConnectWithCode(t *testing.T) {
	e := newUIEnv(t)
	ctx := context.Background()
	admin := e.as(store.RoleAdmin)

	_, body := get(t, admin, e.srv.URL+"/connect/docker", nil)
	if !strings.Contains(body, `name="mode" value="code"`) || !strings.Contains(body, "Get the command") || !strings.Contains(body, "Set up by hand") {
		t.Fatalf("form: %s", body)
	}

	// One click: the command carries a code for one agent.
	_, body, hdr := post(t, admin, e.srv.URL+"/connect/docker", url.Values{"mode": {"code"}, "env": {e.prod.ID}, "expires": {"7d"}})
	if !strings.Contains(body, "glsh_enroll_") || strings.Contains(body, "glsh_agent_") || !strings.Contains(body, "Waiting for the agent to register") ||
		hdr.Get("Cache-Control") != "no-store" {
		t.Fatalf("code page: %s", body)
	}
	codes, _ := e.st.ListEnrollmentCodes(ctx, e.ws.Scope())
	if len(codes) != 1 || !codes[0].Single || codes[0].EnvironmentID != e.prod.ID || codes[0].ExpiresAt.IsZero() {
		t.Fatalf("codes: %+v", codes)
	}
	code := codes[0]

	// The agent enrolls: the page shows it and its target, and keeps polling until the report.
	secret := regexp.MustCompile(`glsh_enroll_[0-9A-Za-z]+`).FindString(body)
	if _, err := e.st.Enroll(ctx, store.Enrollment{
		CodeHash: tokens.Hash(secret), Name: "web-01", TokenHash: "t",
		Targets: []agentproto.Target{{Platform: agentproto.Docker, Name: "web-01", Docker: &agentproto.DockerSettings{DockerHost: "tcp://p:2375"}}},
	}); err != nil {
		t.Fatal(err)
	}
	_, body = get(t, admin, e.srv.URL+"/connect/code/"+code.ID, nil)
	if !strings.Contains(body, "every 3s") || !strings.Contains(body, "Found web-01 (docker)") || !strings.Contains(body, "Waiting for the first report of web-01") {
		t.Fatalf("status after enrolling: %s", body)
	}

	// Many hosts, no expiry, and a cluster name for Kubernetes.
	_, body, _ = post(t, admin, e.srv.URL+"/connect/kubernetes", url.Values{
		"mode": {"code"}, "new_env": {"edge"}, "many": {"on"},
		"expires": {"never"}, "cluster_name": {"prod-eu"},
	})
	if !strings.Contains(body, "--set name=prod-eu") || !strings.Contains(body, "each registers as its own agent") {
		t.Fatalf("many: %s", body)
	}
	codes, _ = e.st.ListEnrollmentCodes(ctx, e.ws.Scope())
	if len(codes) != 2 || codes[0].Single || !codes[0].ExpiresAt.IsZero() {
		t.Fatalf("many code: %+v", codes)
	}
}

func TestCodesOnAgentsPageAndManagedTargets(t *testing.T) {
	e := newUIEnv(t)
	ctx := context.Background()
	admin := e.as(store.RoleAdmin)
	c, err := e.st.CreateEnrollmentCode(ctx, e.ws.Scope(), e.prod.ID, "h", "ana@example.com", time.Time{}, false)
	if err != nil {
		t.Fatal(err)
	}
	res, err := e.st.Enroll(ctx, store.Enrollment{
		CodeHash: "h", Identity: "docker:E", Name: "web-01", TokenHash: "t",
		Targets: []agentproto.Target{{Platform: agentproto.Docker, Name: "web-01", Docker: &agentproto.DockerSettings{DockerHost: "tcp://p:2375"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, body := get(t, admin, e.srv.URL+"/agents", nil)
	if !strings.Contains(body, "Enrollment codes") || !strings.Contains(body, "many agents") || !strings.Contains(body, "ana@example.com") {
		t.Fatalf("agents page: %s", body)
	}

	// The agent's target: settings are read-only and survive a save.
	tid := res.Added[0].ID
	_, body = get(t, admin, e.srv.URL+"/targets/"+tid+"/edit", nil)
	if !strings.Contains(body, "managed by agent web-01") || !strings.Contains(body, "readonly") {
		t.Fatalf("edit: %s", body)
	}
	post(t, admin, e.srv.URL+"/targets/"+tid, url.Values{"environment": {e.prod.ID}, "poll": {"600"}, "settings": {`{"docker":{"docker_host":"tcp://evil:1"}}`}})
	got, _ := e.st.GetTarget(ctx, e.ws.Scope(), tid)
	if !strings.Contains(string(got.Settings), "tcp://p:2375") || got.PollIntervalSeconds != 600 {
		t.Fatalf("saved: %s %d", got.Settings, got.PollIntervalSeconds)
	}
	// Enrolling again keeps the interval set in Goliash.
	if _, err := e.st.Enroll(ctx, store.Enrollment{
		CodeHash: "h", Identity: "docker:E", Name: "web-01", TokenHash: "t2",
		Targets: []agentproto.Target{{Platform: agentproto.Docker, Name: "web-01", Docker: &agentproto.DockerSettings{DockerHost: "tcp://p:2375"}}},
	}); err != nil {
		t.Fatal(err)
	}
	if got, _ := e.st.GetTarget(ctx, e.ws.Scope(), tid); got.PollIntervalSeconds != 600 {
		t.Fatalf("interval reset: %d", got.PollIntervalSeconds)
	}

	post(t, admin, e.srv.URL+"/enroll-codes/"+c.ID+"/revoke", nil)
	if codes, _ := e.st.ListEnrollmentCodes(ctx, e.ws.Scope()); len(codes) != 0 {
		t.Fatalf("not revoked: %+v", codes)
	}
}

func TestConnectLambda(t *testing.T) {
	title, cmd := installCommand("lambda", "https://g", "glsh_enroll_x", nil, "1.14.0")
	if !strings.Contains(title, "functions") || !strings.Contains(cmd, "watch_lambda       = true") || !strings.Contains(cmd, "?ref=v1.14.0") {
		t.Fatalf("lambda command: %s\n%s", title, cmd)
	}
	if _, cmd := installCommand("ecs", "https://g", "t", nil, "1.14.0"); strings.Contains(cmd, "watch_lambda") {
		t.Fatalf("ecs command watches lambda: %s", cmd)
	}
	settings, problem := connectSettings("lambda", url.Values{"region": {"eu-west-1"}, "prefixes": {"shop-, billing-"}, "aliases": {"live=prod, canary=-"}})
	if problem != "" || !strings.Contains(string(settings), `"lambda":{"alias_environments":{"canary":"-","live":"prod"},"name_prefixes":["shop-","billing-"],"region":"eu-west-1"}`) {
		t.Fatalf("settings: %s %s", settings, problem)
	}
	if _, problem := connectSettings("lambda", url.Values{}); problem == "" {
		t.Fatal("lambda without a region")
	}
}

func TestEnrolledAgentPage(t *testing.T) {
	e := newUIEnv(t)
	ctx := context.Background()
	admin := e.as(store.RoleAdmin)
	if _, err := e.st.CreateEnrollmentCode(ctx, e.ws.Scope(), e.prod.ID, "h", "", time.Time{}, true); err != nil {
		t.Fatal(err)
	}
	res, err := e.st.Enroll(ctx, store.Enrollment{
		CodeHash: "h", Name: "web-01", TokenHash: "t",
		Targets: []agentproto.Target{{Platform: agentproto.Docker, Name: "web-01", Docker: &agentproto.DockerSettings{DockerHost: "tcp://p:2375"}}},
		Notes:   []string{"lambda eu-west-1: AccessDeniedException"},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, body := get(t, admin, e.srv.URL+"/agents/"+res.Agent.ID, nil)
	for _, want := range []string{"Registration", "enrollment code for this agent of prod", "Not collected", "AccessDeniedException", "1 target it found go with it"} {
		if !strings.Contains(body, want) {
			t.Fatalf("agent page lacks %q: %s", want, body)
		}
	}
	if strings.Contains(body, "Rotate token") {
		t.Fatal("an enrolled agent offers token rotation")
	}

	// The code page shows the note too.
	codes, _ := e.st.ListEnrollmentCodes(ctx, e.ws.Scope())
	_, body = get(t, admin, e.srv.URL+"/connect/code/"+codes[0].ID+"?p=docker", nil)
	if !strings.Contains(body, "Not collected: lambda eu-west-1") {
		t.Fatalf("code status: %s", body)
	}

	// Deleting it takes the target it found along.
	post(t, admin, e.srv.URL+"/agents/"+res.Agent.ID+"/delete", nil)
	if _, err := e.st.GetAgent(ctx, e.ws.Scope(), res.Agent.ID); err == nil {
		t.Fatal("agent not deleted")
	}
	if _, err := e.st.GetTarget(ctx, e.ws.Scope(), res.Added[0].ID); err == nil {
		t.Fatal("its target is left")
	}
}

func TestAgentLogsHint(t *testing.T) {
	for _, p := range []string{"kubernetes", "docker", "swarm", "nomad", "ecs", "lambda", ""} {
		if agentLogs(p) == "" {
			t.Errorf("%q: no hint", p)
		}
	}
}
