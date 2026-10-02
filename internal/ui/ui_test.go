// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package ui

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/pipozzz/goliash/internal/auth"
	"github.com/pipozzz/goliash/internal/notifier"
	"github.com/pipozzz/goliash/internal/store"
	"github.com/pipozzz/goliash/internal/versions"
)

type uiEnv struct {
	t    *testing.T
	st   *store.Store
	ws   store.Workspace
	srv  *httptest.Server
	auth *auth.Auth
	hub  *Hub
	prod store.Environment
	tgt  store.Target
}

func newUIEnv(t *testing.T) *uiEnv {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, "sqlite://:memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ws, _ := st.EnsureDefaultWorkspace(ctx)
	sc := ws.Scope()
	prod, _ := st.CreateEnvironment(ctx, sc, "prod", 30)
	tgt, _ := st.CreateTarget(ctx, store.Target{Scope: sc, EnvironmentID: prod.ID, Platform: "kubernetes", Name: "k8s-prod"})
	svc, _ := st.EnsureService(ctx, sc, `<img src=x onerror=alert(1)>`)

	snap := store.NewID()
	_, _ = st.InsertSnapshot(ctx, store.Snapshot{ID: snap, Scope: sc, TargetID: tgt.ID, CollectedAt: time.Now(), Complete: true, Payload: json.RawMessage(`{}`)})
	_ = st.ApplySnapshot(ctx, store.SnapshotChanges{Scope: sc, SnapshotID: snap, TargetID: tgt.ID, At: time.Now(), Upsert: []store.Instance{
		{
			TargetID: tgt.ID, EnvironmentID: prod.ID, ServiceID: svc.ID, WorkloadID: "w1", WorkloadKind: "deployment", WorkloadName: "evil",
			ContainerName: "app", Image: "nginx:1.27.2", Tag: "1.27.2", Running: 2, IsMain: true,
		},
		{
			TargetID: tgt.ID, EnvironmentID: prod.ID, WorkloadID: "w2", WorkloadKind: "deployment", WorkloadName: "worker-v2",
			ContainerName: "main", Image: "ghcr.io/acme/worker:2.0.0", Tag: "2.0.0", Running: 1, IsMain: true, SuggestedService: "worker",
		},
	}})

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	mux := http.NewServeMux()
	srv := httptest.NewServer(http.NewCrossOriginProtection().Handler(mux))
	t.Cleanup(srv.Close)
	a, err := auth.New(st, log, srv.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	a.Routes(mux)
	hub := NewHub()
	checker := versions.NewChecker(st, nil, log, time.Hour)
	New(Options{Store: st, Auth: a, Checker: checker, Notifier: notifier.New(st, log, nil), Hub: hub, Log: log, PublicURL: srv.URL}).Register(mux)
	return &uiEnv{t: t, st: st, ws: ws, srv: srv, auth: a, hub: hub, prod: prod, tgt: tgt}
}

// as returns a browser signed in as a new user with the role.
func (e *uiEnv) as(role string) *http.Client {
	e.t.Helper()
	u, err := e.st.CreateUser(context.Background(), e.ws.OrgID, role+"@example.com", "", role)
	if err != nil {
		e.t.Fatal(err)
	}
	link, _ := e.auth.LoginLink(context.Background(), u)
	jar, _ := cookiejar.New(nil)
	c := &http.Client{Jar: jar}
	if code, _ := get(e.t, c, link, nil); code != 200 {
		e.t.Fatalf("sign-in as %s failed: %d", role, code)
	}
	return c
}

func get(t *testing.T, c *http.Client, u string, header map[string]string) (int, string) {
	t.Helper()
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, u, nil)
	for k, v := range header {
		req.Header.Set(k, v)
	}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func post(t *testing.T, c *http.Client, u string, form url.Values) (int, string, http.Header) {
	t.Helper()
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, u, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b), resp.Header
}

func TestSignInRequired(t *testing.T) {
	e := newUIEnv(t)
	noRedirect := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, e.srv.URL+"/", nil)
	resp, err := noRedirect.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/login" {
		t.Fatalf("anonymous home: %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}
	req, _ = http.NewRequestWithContext(context.Background(), http.MethodGet, e.srv.URL+"/ui/matrix", nil)
	req.Header.Set("HX-Request", "true")
	resp, _ = noRedirect.Do(req)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized || resp.Header.Get("HX-Redirect") != "/login" {
		t.Fatalf("anonymous htmx: %d %q", resp.StatusCode, resp.Header.Get("HX-Redirect"))
	}
	if code, body := get(t, http.DefaultClient, e.srv.URL+"/login", nil); code != 200 || !strings.Contains(body, "goliash login-link") {
		t.Fatalf("login page: %d", code)
	}
}

func TestViewer(t *testing.T) {
	e := newUIEnv(t)
	c := e.as(store.RoleViewer)

	code, body := get(t, c, e.srv.URL+"/", nil)
	if code != 200 || !strings.Contains(body, "1.27.2") || !strings.Contains(body, "k8s-prod") {
		t.Fatalf("matrix: %d", code)
	}
	if strings.Contains(body, "<img src=x") || !strings.Contains(body, "&lt;img src=x onerror=alert(1)&gt;") {
		t.Fatal("service name not escaped")
	}
	if !strings.Contains(body, `/static/app.css?v=`+assetVersion) {
		t.Fatal("assets not versioned")
	}
	if code, body := get(t, c, e.srv.URL+"/inbox", nil); code != 200 || !strings.Contains(body, "worker-v2") || strings.Contains(body, `action="/inbox/map"`) {
		t.Fatalf("viewer inbox: %d (map form must be hidden)", code)
	}
	if code, _, _ := post(t, c, e.srv.URL+"/inbox/map", url.Values{"service": {"worker"}}); code != http.StatusForbidden {
		t.Fatalf("viewer mapped: %d", code)
	}
	if code, _ := get(t, c, e.srv.URL+"/settings", nil); code != http.StatusForbidden {
		t.Fatalf("viewer settings: %d", code)
	}
	for _, p := range []string{"/events", "/agents", "/notifications", "/services/" + url.PathEscape(`<img src=x onerror=alert(1)>`)} {
		if code, _ := get(t, c, e.srv.URL+p, nil); code != 200 {
			t.Errorf("%s: %d", p, code)
		}
	}
	if code, _ := get(t, c, e.srv.URL+"/services/missing", nil); code != 404 {
		t.Errorf("missing service: %d", code)
	}
}

func TestMemberMapsInboxAndEditsPolicy(t *testing.T) {
	e := newUIEnv(t)
	c := e.as(store.RoleMember)
	ctx := context.Background()

	_, body, _ := post(t, c, e.srv.URL+"/inbox/map", url.Values{
		"service": {"worker"}, "target_id": {e.tgt.ID}, "workload_id": {"w2"}, "repo": {"ghcr.io/acme/worker"},
	})
	if !strings.Contains(body, "Mapped to worker") {
		t.Fatalf("map result: %s", body)
	}
	rules, _ := e.st.ListMappingRules(ctx, e.ws.Scope())
	if len(rules) != 1 || rules[0].Pattern != `ghcr\.io/acme/worker` || rules[0].MatchType != "image_repo" {
		t.Fatalf("rule %+v", rules)
	}
	if _, body := get(t, c, e.srv.URL+"/", nil); !strings.Contains(body, `href="/services/worker"`) {
		t.Fatal("mapped service missing from matrix")
	}
	if _, body, _ := post(t, c, e.srv.URL+"/inbox/map", url.Values{"service": {"bad name!"}}); !strings.Contains(body, "Service names use") {
		t.Fatal("invalid service name accepted")
	}

	_, body, _ = post(t, c, e.srv.URL+"/services/worker/policy", url.Values{
		"owner": {"team-jobs"}, "kind": {"own"}, "track": {"minor"}, "pin_major": {"2"}, "tag_filter": {`^\d+\.\d+\.\d+$`},
	})
	if !strings.Contains(body, "Policy saved") || !strings.Contains(body, `value="team-jobs"`) {
		t.Fatalf("policy page: %s", body)
	}
	svc, _ := e.st.GetServiceByName(ctx, e.ws.Scope(), "worker")
	p, _ := versions.ParsePolicy(svc.VersionPolicy)
	if svc.Owner != "team-jobs" || p.Track != versions.JumpMinor || *p.PinMajor != 2 {
		t.Fatalf("saved %+v %+v", svc, p)
	}
	if _, body, _ := post(t, c, e.srv.URL+"/services/worker/policy", url.Values{"tag_filter": {"("}}); !strings.Contains(body, "Policy not saved") {
		t.Fatal("bad regexp accepted")
	}

	_, body, _ = post(t, c, e.srv.URL+"/services/worker/ack", url.Values{"kind": {"release"}, "until_version": {"3.0.0"}})
	if !strings.Contains(body, "Acknowledged") || !strings.Contains(body, "version 3.0.0") {
		t.Fatal("ack not shown")
	}
	if code, _, _ := post(t, c, e.srv.URL+"/agents", url.Values{"name": {"x"}}); code != http.StatusForbidden {
		t.Fatalf("member created an agent: %d", code)
	}
}

func TestAdminAgentsUsersAndTokens(t *testing.T) {
	e := newUIEnv(t)
	c := e.as(store.RoleAdmin)
	ctx := context.Background()

	_, body, hdr := post(t, c, e.srv.URL+"/agents", url.Values{"name": {"eu-cluster"}})
	if !strings.Contains(body, "glsh_agent_") || hdr.Get("Cache-Control") != "no-store" {
		t.Fatal("agent token not shown once")
	}
	if _, body, _ = post(t, c, e.srv.URL+"/environments", url.Values{"name": {"staging"}, "position": {"20"}}); !strings.Contains(body, "Environment staging created") {
		t.Fatal("environment")
	}
	_, body, _ = post(t, c, e.srv.URL+"/targets", url.Values{
		"name": {"stg-1"}, "platform": {"swarm"}, "environment": {"staging"},
		"agent": {"eu-cluster"}, "settings": {`{"swarm":{"docker_host":"tcp://proxy:2375"}}`},
	})
	if !strings.Contains(body, "Target stg-1 created") {
		t.Fatalf("target: %s", body)
	}
	if _, body, _ = post(t, c, e.srv.URL+"/targets", url.Values{"name": {"x"}, "platform": {"swarm"}, "environment": {"staging"}, "settings": {"{"}}); !strings.Contains(body, "Settings must be a JSON object") {
		t.Fatal("bad settings accepted")
	}

	_, body, _ = post(t, c, e.srv.URL+"/settings/users", url.Values{"email": {"dev@example.com"}, "role": {"member"}})
	if !strings.Contains(body, "/auth/magic?token=") || !strings.Contains(body, "dev@example.com invited as member") {
		t.Fatal("invite link not shown")
	}
	if _, body, _ = post(t, c, e.srv.URL+"/settings/users", url.Values{"email": {"boss@example.com"}, "role": {"owner"}}); !strings.Contains(body, "allowed to give") {
		t.Fatal("admin made an owner")
	}
	dev, _ := e.st.GetUserByEmail(ctx, e.ws.OrgID, "dev@example.com")
	if _, body, _ = post(t, c, e.srv.URL+"/settings/users/"+dev.ID+"/role", url.Values{"role": {"viewer"}}); !strings.Contains(body, "is now viewer") {
		t.Fatal("role change")
	}
	self, _ := e.st.GetUserByEmail(ctx, e.ws.OrgID, "admin@example.com")
	if _, body, _ = post(t, c, e.srv.URL+"/settings/users/"+self.ID+"/role", url.Values{"role": {"owner"}}); !strings.Contains(body, "allowed to give") && !strings.Contains(body, "cannot change") {
		t.Fatal("admin promoted themself")
	}
	if _, body, _ = post(t, c, e.srv.URL+"/settings/tokens", url.Values{"name": {"prom"}}); !strings.Contains(body, "glsh_api_") {
		t.Fatal("api token not shown")
	}
	if _, body, _ = post(t, c, e.srv.URL+"/notifications/channels", url.Values{"name": {"ops"}, "type": {"slack"}, "url": {"https://hooks.slack.com/services/T/B/SECRET"}}); !strings.Contains(body, "Channel ops added") || strings.Contains(body, "SECRET") {
		t.Fatal("channel added or its URL leaked into the page")
	}
}

func TestStream(t *testing.T) {
	e := newUIEnv(t)
	c := e.as(store.RoleViewer)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, e.srv.URL+"/ui/stream", nil)
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("content type %q", resp.Header.Get("Content-Type"))
	}
	r := bufio.NewReader(resp.Body)
	if line, _ := r.ReadString('\n'); !strings.HasPrefix(line, "retry:") {
		t.Fatalf("first line %q", line)
	}
	go func() {
		time.Sleep(100 * time.Millisecond)
		e.hub.Publish("other-workspace") // must not reach this stream
		e.hub.Publish(e.ws.ID)
	}()
	deadline := time.After(5 * time.Second)
	got := make(chan string, 1)
	go func() {
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			if strings.HasPrefix(line, "event:") {
				got <- strings.TrimSpace(line)
				return
			}
		}
	}()
	select {
	case ev := <-got:
		if ev != "event: changed" {
			t.Fatalf("event %q", ev)
		}
	case <-deadline:
		t.Fatal("no event")
	}
}

func TestCrossOriginFormRejected(t *testing.T) {
	e := newUIEnv(t)
	c := e.as(store.RoleAdmin)
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, e.srv.URL+"/agents", strings.NewReader("name=evil"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", "https://evil.example")
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-site form: %d", resp.StatusCode)
	}
	if agents, _ := e.st.ListAgents(context.Background(), e.ws.Scope()); len(agents) != 0 {
		t.Fatal("cross-site request created an agent")
	}
}
