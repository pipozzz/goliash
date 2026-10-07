// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/pipozzz/goliash/internal/tokens"
	"github.com/pipozzz/goliash/pkg/agentproto"
)

func TestEnroll(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	envs, err := e.st.ListEnvironments(ctx, e.ws.Scope())
	if err != nil {
		t.Fatal(err)
	}
	code, hash := tokens.New(tokens.Enroll)
	c, err := e.st.CreateEnrollmentCode(ctx, e.ws.Scope(), envs[0].ID, hash, "test", time.Now().Add(time.Hour), false)
	if err != nil {
		t.Fatal(err)
	}
	enrollWith := func(code, identity, name string, targets ...agentproto.DeclaredTarget) (result, agentproto.EnrollResponse) {
		t.Helper()
		if targets == nil {
			targets = []agentproto.DeclaredTarget{}
		}
		resp, body := e.do(request{method: "POST", path: "/agent/v1/enroll", token: "-", body: agentproto.EnrollRequest{
			Code: code, Identity: identity, Name: name, Version: "1.14.0", Hostname: "h", Targets: targets,
		}})
		var got agentproto.EnrollResponse
		if resp.StatusCode == http.StatusOK {
			if err := json.Unmarshal(body, &got); err != nil {
				t.Fatal(err)
			}
		}
		return resp, got
	}
	enroll := func(identity, name string, targets ...agentproto.DeclaredTarget) (result, agentproto.EnrollResponse) {
		t.Helper()
		return enrollWith(code, identity, name, targets...)
	}
	docker := func(host string) agentproto.DeclaredTarget {
		return agentproto.DeclaredTarget{
			Platform: agentproto.Docker, Name: "web-01",
			Docker: &agentproto.DockerSettings{DockerHost: host},
		}
	}

	// A new installation gets an agent and its targets.
	resp, first := enroll("docker:ENGINE1", "web-01", docker("tcp://socket-proxy:2375"))
	if resp.StatusCode != http.StatusOK || first.Name != "web-01" || !tokens.Valid(first.Token, tokens.Agent) {
		t.Fatalf("enroll: %d %+v", resp.StatusCode, first)
	}
	ts, err := e.st.ListAgentTargets(ctx, e.ws.Scope(), first.AgentID)
	if err != nil || len(ts) != 1 || ts[0].Platform != "docker" || ts[0].EnvironmentID != envs[0].ID || ts[0].AgentKey != "docker:web-01" {
		t.Fatalf("targets: %+v %v", ts, err)
	}
	// The token works.
	r, body := e.do(request{method: "GET", path: "/agent/v1/config", token: first.Token})
	expectStatus(t, r, body, http.StatusOK)

	// The same installation enrolls again: same agent, new token, the old one stops
	// working, and the target takes the new settings.
	_, again := enroll("docker:ENGINE1", "web-01", docker("unix:///var/run/docker.sock"))
	if again.AgentID != first.AgentID || again.Token == first.Token {
		t.Fatalf("re-enroll: %+v", again)
	}
	r, body = e.do(request{method: "GET", path: "/agent/v1/config", token: first.Token})
	expectStatus(t, r, body, http.StatusUnauthorized)
	ts, _ = e.st.ListAgentTargets(ctx, e.ws.Scope(), first.AgentID)
	if len(ts) != 1 || string(ts[0].Settings) != `{"docker":{"docker_host":"unix:///var/run/docker.sock"}}` {
		t.Fatalf("settings not updated: %+v", ts)
	}

	// Another installation with the same name gets a suffix, for agent and target.
	_, other := enroll("docker:ENGINE2", "web-01", docker("tcp://x:2375"))
	if other.AgentID == first.AgentID || other.Name != "web-01-2" {
		t.Fatalf("second agent: %+v", other)
	}

	// An expired code admits known installations only.
	expired, hash2 := tokens.New(tokens.Enroll)
	if _, err := e.st.CreateEnrollmentCode(ctx, e.ws.Scope(), envs[0].ID, hash2, "test", time.Now().Add(-time.Minute), false); err != nil {
		t.Fatal(err)
	}
	if resp, _ := enrollWith(expired, "docker:ENGINE3", "new"); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expired code admitted a new agent: %d", resp.StatusCode)
	}
	if resp, _ := enrollWith(expired, "docker:ENGINE1", "web-01"); resp.StatusCode != http.StatusOK {
		t.Fatalf("expired code refused a known agent: %d", resp.StatusCode)
	}

	// A revoked agent stays out.
	if err := e.st.RevokeAgentTokens(ctx, e.ws.Scope(), other.AgentID); err != nil {
		t.Fatal(err)
	}
	if err := e.st.RegisterAgent(ctx, e.ws.Scope(), other.AgentID, "1.14.0", "h", nil); err != nil {
		t.Fatal(err)
	}
	if resp, _ := enroll("docker:ENGINE2", "web-01"); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("revoked agent enrolled: %d", resp.StatusCode)
	}

	// A revoked code admits nobody.
	if err := e.st.RevokeEnrollmentCode(ctx, e.ws.Scope(), c.ID); err != nil {
		t.Fatal(err)
	}
	if resp, _ := enroll("docker:ENGINE1", "web-01"); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("revoked code: %d", resp.StatusCode)
	}
}

func TestEnrollRateLimit(t *testing.T) {
	e := newEnv(t)
	bad, _ := tokens.New(tokens.Enroll)
	var last result
	for range enrollFailures + 1 {
		last, _ = e.do(request{method: "POST", path: "/agent/v1/enroll", token: "-", body: agentproto.EnrollRequest{
			Code: bad, Identity: "x", Name: "x", Version: "1", Hostname: "h", Targets: []agentproto.DeclaredTarget{},
		}})
	}
	if last.StatusCode != http.StatusTooManyRequests || last.Header.Get("Retry-After") == "" {
		t.Fatalf("not limited: %d", last.StatusCode)
	}
}
