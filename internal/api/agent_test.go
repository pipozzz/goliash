// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/getkin/kin-openapi/routers"

	"github.com/pipozzz/goliash/internal/ingest"
	"github.com/pipozzz/goliash/internal/store"
	"github.com/pipozzz/goliash/internal/tokens"
	"github.com/pipozzz/goliash/pkg/agentproto"
)

type env struct {
	t      *testing.T
	h      *AgentHandler
	srv    *httptest.Server
	st     *store.Store
	ws     store.Workspace
	agent  store.Agent
	token  string
	target store.Target
}

func newEnv(t *testing.T) *env {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, "sqlite://:memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	ws, err := st.EnsureDefaultWorkspace(ctx)
	if err != nil {
		t.Fatal(err)
	}
	prod, err := st.CreateEnvironment(ctx, ws.Scope(), "prod", 30)
	if err != nil {
		t.Fatal(err)
	}
	token, hash := tokens.New(tokens.Agent)
	agent, err := st.CreateAgent(ctx, ws.Scope(), "eu-cluster", hash)
	if err != nil {
		t.Fatal(err)
	}
	target, err := st.CreateTarget(ctx, store.Target{
		Scope: ws.Scope(), EnvironmentID: prod.ID, AgentID: agent.ID, Platform: "kubernetes", Name: "prod-eu-1",
		Settings: json.RawMessage(`{"kubernetes":{"exclude_namespaces":["kube-system"]}}`),
	})
	if err != nil {
		t.Fatal(err)
	}

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	h, err := NewAgentHandler(st, ingest.New(st, log), log)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	h.Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	return &env{t: t, h: h, srv: srv, st: st, ws: ws, agent: agent, token: token, target: target}
}

type request struct {
	method, path string
	body         any
	gzip         bool
	token        string
	header       map[string]string
}

// result is a received response with its body already read and closed.
type result struct {
	StatusCode int
	Header     http.Header
}

// do sends the request and checks the response against the spec.
func (e *env) do(r request) (result, []byte) {
	e.t.Helper()
	var body io.Reader
	var raw []byte
	if r.body != nil {
		switch b := r.body.(type) {
		case []byte:
			raw = b
		default:
			var err error
			if raw, err = json.Marshal(b); err != nil {
				e.t.Fatal(err)
			}
		}
		if r.gzip {
			var buf bytes.Buffer
			zw := gzip.NewWriter(&buf)
			_, _ = zw.Write(raw)
			_ = zw.Close()
			body = &buf
		} else {
			body = bytes.NewReader(raw)
		}
	}
	req, err := http.NewRequestWithContext(context.Background(), r.method, e.srv.URL+r.path, body)
	if err != nil {
		e.t.Fatal(err)
	}
	if r.body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if r.gzip {
		req.Header.Set("Content-Encoding", "gzip")
	}
	token := e.token
	if r.token != "" {
		token = r.token
	}
	if token != "-" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for k, v := range r.header {
		req.Header.Set(k, v)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		e.t.Fatal(err)
	}
	res := result{StatusCode: resp.StatusCode, Header: resp.Header}
	e.checkResponse(r.method, r.path, raw, res, respBody)
	return res, respBody
}

// checkResponse fails the test when a response is not allowed by the spec.
func (e *env) checkResponse(method, path string, reqBody []byte, resp result, body []byte) {
	e.t.Helper()
	item := e.h.spec.Paths.Find(path)
	if item == nil || item.GetOperation(method) == nil {
		return // unknown endpoint, nothing to check against
	}
	req := httptest.NewRequestWithContext(context.Background(), method, path, bytes.NewReader(reqBody))
	err := openapi3filter.ValidateResponse(context.Background(), &openapi3filter.ResponseValidationInput{
		RequestValidationInput: &openapi3filter.RequestValidationInput{
			Request: req,
			Route: &routers.Route{
				Spec: e.h.spec, Path: path, PathItem: item, Method: method,
				Operation: item.GetOperation(method),
			},
		},
		Status: resp.StatusCode,
		Header: resp.Header,
		Body:   io.NopCloser(bytes.NewReader(body)),
	})
	if err != nil {
		e.t.Fatalf("%s %s: response %d violates the spec: %v\n%s", method, path, resp.StatusCode, err, body)
	}
}

func expectStatus(t *testing.T, resp result, body []byte, want int) {
	t.Helper()
	if resp.StatusCode != want {
		t.Fatalf("status = %d, want %d: %s", resp.StatusCode, want, body)
	}
}

func (e *env) snapshot(id string, targetID string) agentproto.Snapshot {
	return agentproto.Snapshot{
		SnapshotID: id, TargetID: targetID, CollectedAt: time.Now().UTC().Truncate(time.Second), Complete: true,
		Workloads: []agentproto.Workload{{
			ID: "uid-1", Kind: agentproto.Deployment, Name: "payments-api",
			Containers: []agentproto.Container{{Name: "app", Image: "ghcr.io/acme/payments-api:1.4.2", Running: 3}},
		}},
	}
}

func TestAuthentication(t *testing.T) {
	e := newEnv(t)
	other, _ := tokens.New(tokens.Agent)
	ciToken, _ := tokens.New(tokens.CI)

	for name, token := range map[string]string{
		"missing":    "-",
		"garbage":    "hello",
		"wrong kind": ciToken,
		"bad sum":    e.token[:len(e.token)-1] + "x",
		"not issued": other,
	} {
		t.Run(name, func(t *testing.T) {
			resp, body := e.do(request{method: "GET", path: "/agent/v1/config", token: token})
			expectStatus(t, resp, body, http.StatusUnauthorized)
			if ct := resp.Header.Get("Content-Type"); ct != "application/problem+json" {
				t.Fatalf("content type %q", ct)
			}
		})
	}

	if err := e.st.RevokeAgentTokens(context.Background(), e.ws.Scope(), e.agent.ID); err != nil {
		t.Fatal(err)
	}
	resp, body := e.do(request{method: "GET", path: "/agent/v1/config"})
	expectStatus(t, resp, body, http.StatusUnauthorized)
}

func TestRegister(t *testing.T) {
	e := newEnv(t)
	resp, body := e.do(request{method: "POST", path: "/agent/v1/register", body: agentproto.RegisterRequest{
		Version: "0.1.0", Hostname: "node-1", Platforms: []agentproto.Platform{agentproto.Kubernetes},
	}})
	expectStatus(t, resp, body, http.StatusOK)

	var got agentproto.RegisterResponse
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	if got.AgentID != e.agent.ID || got.WorkspaceID != e.ws.ID {
		t.Fatalf("response %+v", got)
	}
	a, err := e.st.GetAgent(context.Background(), e.ws.Scope(), e.agent.ID)
	if err != nil || a.Version != "0.1.0" || a.Hostname != "node-1" || a.RegisteredAt.IsZero() {
		t.Fatalf("agent not updated: %+v %v", a, err)
	}

	resp, body = e.do(request{
		method: "POST", path: "/agent/v1/register",
		body: map[string]any{"version": "0.1.0", "hostname": "x", "platforms": []string{"openshift"}},
	})
	expectStatus(t, resp, body, http.StatusBadRequest)
}

func TestConfigAndETag(t *testing.T) {
	e := newEnv(t)
	resp, body := e.do(request{method: "GET", path: "/agent/v1/config"})
	expectStatus(t, resp, body, http.StatusOK)
	etag := resp.Header.Get("ETag")
	if etag == "" {
		t.Fatal("no ETag")
	}

	var cfg agentproto.AgentConfig
	if err := json.Unmarshal(body, &cfg); err != nil {
		t.Fatal(err)
	}
	if len(cfg.Targets) != 1 {
		t.Fatalf("targets = %+v", cfg.Targets)
	}
	tg := cfg.Targets[0]
	if tg.ID != e.target.ID || tg.Kubernetes == nil || tg.Kubernetes.ExcludeNamespaces[0] != "kube-system" ||
		tg.DebounceSeconds == nil || *tg.DebounceSeconds != 30 || tg.PollIntervalSeconds != 300 {
		t.Fatalf("target = %+v", tg)
	}

	resp, body = e.do(request{method: "GET", path: "/agent/v1/config", header: map[string]string{"If-None-Match": etag}})
	expectStatus(t, resp, body, http.StatusNotModified)

	// A new target changes the ETag.
	envs, _ := e.st.ListEnvironments(context.Background(), e.ws.Scope())
	if _, err := e.st.CreateTarget(context.Background(), store.Target{
		Scope: e.ws.Scope(), EnvironmentID: envs[0].ID,
		AgentID: e.agent.ID, Platform: "ecs", Name: "ecs-prod", Settings: json.RawMessage(`{"ecs":{"region":"eu-west-1"}}`),
	}); err != nil {
		t.Fatal(err)
	}
	resp, body = e.do(request{method: "GET", path: "/agent/v1/config", header: map[string]string{"If-None-Match": etag}})
	expectStatus(t, resp, body, http.StatusOK)
	if resp.Header.Get("ETag") == etag {
		t.Fatal("ETag did not change")
	}
}

func TestSnapshot(t *testing.T) {
	e := newEnv(t)
	snap := e.snapshot(store.NewID(), e.target.ID)

	resp, body := e.do(request{method: "POST", path: "/agent/v1/snapshot", body: snap, gzip: true})
	expectStatus(t, resp, body, http.StatusAccepted)
	var ack agentproto.SnapshotAck
	_ = json.Unmarshal(body, &ack)
	if ack.Status != agentproto.Accepted || ack.SnapshotID != snap.SnapshotID {
		t.Fatalf("ack = %+v", ack)
	}

	resp, body = e.do(request{method: "POST", path: "/agent/v1/snapshot", body: snap})
	expectStatus(t, resp, body, http.StatusAccepted)
	_ = json.Unmarshal(body, &ack)
	if ack.Status != agentproto.Duplicate {
		t.Fatalf("second send: %+v", ack)
	}

	stored, err := e.st.UnprocessedSnapshots(context.Background(), 10)
	if err != nil || len(stored) != 1 {
		t.Fatalf("stored = %+v %v", stored, err)
	}
	var payload agentproto.Snapshot
	if err := json.Unmarshal(stored[0].Payload, &payload); err != nil || payload.Workloads[0].Name != "payments-api" {
		t.Fatalf("payload not stored decompressed: %s", stored[0].Payload)
	}
}

func TestSnapshotRejected(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()

	// A target that belongs to another agent of the same workspace.
	_, otherHash := tokens.New(tokens.Agent)
	other, err := e.st.CreateAgent(ctx, e.ws.Scope(), "other", otherHash)
	if err != nil {
		t.Fatal(err)
	}
	foreign, err := e.st.CreateTarget(ctx, store.Target{
		Scope: e.ws.Scope(), EnvironmentID: e.target.EnvironmentID,
		AgentID: other.ID, Platform: "nomad", Name: "nomad", Settings: json.RawMessage(`{"nomad":{"address":"http://n:4646"}}`),
	})
	if err != nil {
		t.Fatal(err)
	}

	bad := e.snapshot("not-a-ulid", e.target.ID)
	tooBig := bytes.Repeat([]byte("x"), maxSnapshotBody+1)

	cases := []struct {
		name string
		req  request
		want int
	}{
		{"foreign target", request{body: e.snapshot(store.NewID(), foreign.ID)}, http.StatusNotFound},
		{"unknown target", request{body: e.snapshot(store.NewID(), store.NewID())}, http.StatusNotFound},
		{"schema violation", request{body: bad}, http.StatusBadRequest},
		{"not json", request{body: []byte("{")}, http.StatusBadRequest},
		{"too large", request{body: tooBig}, http.StatusRequestEntityTooLarge},
		{"bad gzip", request{body: []byte("plain"), header: map[string]string{"Content-Encoding": "gzip"}}, http.StatusBadRequest},
		{"unknown encoding", request{body: e.snapshot(store.NewID(), e.target.ID), header: map[string]string{"Content-Encoding": "br"}}, http.StatusBadRequest},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			c.req.method, c.req.path = "POST", "/agent/v1/snapshot"
			resp, body := e.do(c.req)
			expectStatus(t, resp, body, c.want)
		})
	}
	if stored, _ := e.st.UnprocessedSnapshots(ctx, 10); len(stored) != 0 {
		t.Fatalf("rejected snapshots were stored: %d", len(stored))
	}
}

func TestDecompressionBomb(t *testing.T) {
	e := newEnv(t)
	var buf bytes.Buffer
	zw, _ := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	chunk := bytes.Repeat([]byte(" "), 1<<20)
	for range (maxSnapshotDecoded >> 20) + 1 {
		_, _ = zw.Write(chunk)
	}
	_ = zw.Close()
	if buf.Len() > maxSnapshotBody {
		t.Fatalf("test payload too large to reach the decoder: %d", buf.Len())
	}
	resp, body := e.do(request{
		method: "POST", path: "/agent/v1/snapshot", body: buf.Bytes(),
		header: map[string]string{"Content-Encoding": "gzip"},
	})
	expectStatus(t, resp, body, http.StatusRequestEntityTooLarge)
}

func TestHeartbeat(t *testing.T) {
	e := newEnv(t)
	msg := "namespace billing: forbidden"
	resp, body := e.do(request{method: "POST", path: "/agent/v1/heartbeat", body: agentproto.Heartbeat{
		SentAt: time.Now().UTC(),
		Collectors: []agentproto.CollectorStatus{
			{TargetID: e.target.ID, Status: agentproto.Degraded, LastError: &msg},
			{TargetID: store.NewID(), Status: agentproto.Ok}, // not ours: ignored
		},
	}})
	expectStatus(t, resp, body, http.StatusOK)

	var hb agentproto.HeartbeatResponse
	_ = json.Unmarshal(body, &hb)
	cfgResp, cfgBody := e.do(request{method: "GET", path: "/agent/v1/config"})
	expectStatus(t, cfgResp, cfgBody, http.StatusOK)
	if hb.ConfigEtag != cfgResp.Header.Get("ETag") {
		t.Fatalf("heartbeat etag %s != config etag %s", hb.ConfigEtag, cfgResp.Header.Get("ETag"))
	}

	tg, err := e.st.GetTarget(context.Background(), e.ws.Scope(), e.target.ID)
	if err != nil || tg.CollectorStatus != "degraded" || tg.CollectorError != msg {
		t.Fatalf("collector status not stored: %+v %v", tg, err)
	}
	a, err := e.st.GetAgent(context.Background(), e.ws.Scope(), e.agent.ID)
	if err != nil || a.LastSeenAt.IsZero() {
		t.Fatalf("last_seen_at not set: %+v %v", a, err)
	}
}

func TestRegistryResults(t *testing.T) {
	e := newEnv(t)
	resp, body := e.do(request{method: "POST", path: "/agent/v1/registry-results", body: agentproto.RegistryResults{
		CheckedAt: time.Now().UTC(),
		Results:   []agentproto.RegistryResult{{Repository: "registry.example.com/team/app", Tags: []agentproto.RegistryTag{{Name: "1.0.0"}}}},
	}})
	expectStatus(t, resp, body, http.StatusAccepted)
}

func TestUnknownEndpoint(t *testing.T) {
	e := newEnv(t)
	resp, body := e.do(request{method: "GET", path: "/agent/v1/nope"})
	expectStatus(t, resp, body, http.StatusNotFound)
	if !strings.Contains(resp.Header.Get("Content-Type"), "problem+json") {
		t.Fatal("not a problem response")
	}
}
