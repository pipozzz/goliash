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
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/pipozzz/goliash/internal/auth"
	"github.com/pipozzz/goliash/internal/mapping"
	"github.com/pipozzz/goliash/internal/notifier"
	"github.com/pipozzz/goliash/internal/store"
	"github.com/pipozzz/goliash/internal/tokens"
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
	if !store.OrgWide(role) {
		_ = e.st.SetMembership(context.Background(), u.ID, e.ws.ID, role)
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
	for _, p := range []string{"/events", "/agents", "/notifications", "/delivery", "/report", "/report?month=2026-01", "/?at=2026-01-01T00:00", "/services/" + url.PathEscape(`<img src=x onerror=alert(1)>`)} {
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
	if !strings.Contains(body, "Mapped 1 workload to worker") {
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
	if !strings.Contains(body, "/auth/magic?token=") || !strings.Contains(body, "dev@example.com invited (member)") {
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
	if _, body, _ = post(t, c, e.srv.URL+"/settings/tokens", url.Values{"name": {"prom"}}); !strings.Contains(body, "Choose viewer or member.") {
		t.Fatal("token without a role")
	}
	if _, body, _ = post(t, c, e.srv.URL+"/settings/tokens", url.Values{"name": {"prom"}, "role": {"admin"}}); !strings.Contains(body, "Choose viewer or member.") {
		t.Fatal("admin token created")
	}
	_, body, _ = post(t, c, e.srv.URL+"/settings/tokens", url.Values{"name": {"prom"}, "role": {"viewer"}, "expires": {"90"}})
	if !strings.Contains(body, "API token prom (viewer) for") || !regexp.MustCompile(`glsh_api_[A-Za-z0-9]{20,}`).MatchString(body) {
		t.Fatal("api token not shown")
	}
	toks, _ := e.st.ListAPITokens(ctx, e.ws.Scope())
	if len(toks) != 1 || toks[0].Role != "viewer" || toks[0].CreatedBy != "admin@example.com" ||
		toks[0].ExpiresAt.Before(time.Now().Add(89*24*time.Hour)) {
		t.Fatalf("tokens %+v", toks)
	}
	if _, page := get(t, c, e.srv.URL+"/settings", nil); !strings.Contains(page, "by admin@example.com") || !strings.Contains(page, "Revoke") {
		t.Fatalf("token list: %s", page[strings.Index(page, "<h2>API tokens"):])
	}
	if _, body, _ = post(t, c, e.srv.URL+"/settings/tokens/"+toks[0].ID+"/revoke", nil); !strings.Contains(body, "Token prom revoked.") {
		t.Fatalf("revoke: %s", body)
	}
	if _, body, _ = post(t, c, e.srv.URL+"/settings/tokens/"+toks[0].ID+"/revoke", nil); !strings.Contains(body, "already revoked") {
		t.Fatal("revoked twice")
	}
	if _, body, _ = post(t, c, e.srv.URL+"/notifications/channels", url.Values{"name": {"ops"}, "type": {"slack"}, "url": {"https://hooks.slack.com/services/T/B/SECRET"}}); !strings.Contains(body, "Channel ops added") || strings.Contains(body, "SECRET") {
		t.Fatal("channel added or its URL leaked into the page")
	}

	_, page := get(t, c, e.srv.URL+"/settings", nil)
	for _, want := range []string{
		"user.sign_in", "agent.create", "agent=eu-cluster", "target.create", "user.invite", "user.role",
		"api_token.create", "role=viewer", "api_token.revoke", "channel.create",
	} {
		if !strings.Contains(page, want) {
			t.Errorf("audit log misses %s", want)
		}
	}
	if regexp.MustCompile(`glsh_(agent|api)_[A-Za-z0-9]{20}`).MatchString(page) || strings.Contains(page, "SECRET") {
		t.Error("a secret reached the audit log")
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

// An MSP with a client workspace: the client sees only their own data and people.
func TestWorkspaceIsolationForClients(t *testing.T) {
	e := newUIEnv(t)
	ctx := context.Background()
	admin := e.as(store.RoleAdmin)

	// The organization admin creates the client workspace in the UI.
	if _, body, _ := post(t, admin, e.srv.URL+"/workspaces", url.Values{"name": {"Client A"}, "slug": {"client-a"}, "envs": {"1"}}); !strings.Contains(body, "Workspace Client A created") {
		t.Fatalf("create workspace: %s", body)
	}
	clientWS, err := e.st.GetWorkspaceBySlug(ctx, e.ws.OrgID, "client-a")
	if err != nil {
		t.Fatal(err)
	}
	if envs, _ := e.st.ListEnvironments(ctx, clientWS.Scope()); len(envs) != 3 {
		t.Fatalf("environments %+v", envs)
	}

	// The admin switches to it and invites the client as workspace admin.
	if _, body, _ := post(t, admin, e.srv.URL+"/workspace", url.Values{"workspace": {clientWS.ID}}); !strings.Contains(body, "Switched to Client A") {
		t.Fatal("switch")
	}
	_, body, _ := post(t, admin, e.srv.URL+"/settings/users", url.Values{"email": {"ops@client-a.example"}, "role": {"admin"}})
	if !strings.Contains(body, "/auth/magic?token=") {
		t.Fatalf("invite: %s", body)
	}
	link := regexp.MustCompile(`http://[^"<\s]+/auth/magic\?token=[A-Za-z0-9_-]+`).FindString(body)
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}
	get(t, client, link, nil)

	// The client works in Client A: no services from the MSP's own workspace.
	_, page := get(t, client, e.srv.URL+"/", nil)
	if strings.Contains(page, "k8s-prod") || strings.Contains(page, "onerror") || !strings.Contains(page, "Client A") {
		t.Fatal("client sees another workspace's data")
	}
	if strings.Contains(page, `name="workspace"`) {
		t.Fatal("client got a workspace switcher")
	}
	// Forcing a switch into the MSP workspace fails.
	if _, body, _ := post(t, client, e.srv.URL+"/workspace", url.Values{"workspace": {e.ws.ID}}); !strings.Contains(body, "You cannot open that workspace") {
		t.Fatal("client switched into a foreign workspace")
	}
	// As workspace admin the client manages Client A's people only.
	_, page = get(t, client, e.srv.URL+"/settings", nil)
	if !strings.Contains(page, "<strong>ops@client-a.example</strong>") || strings.Contains(page, "<strong>admin@example.com</strong>") || strings.Contains(page, "org-admin") {
		t.Fatal("client admin sees organization people or org roles")
	}
	if strings.Contains(page, "workspace.create") {
		t.Fatal("client admin sees organization-level audit entries")
	}
	if code, _ := get(t, client, e.srv.URL+"/workspaces", nil); code != http.StatusForbidden {
		t.Fatalf("client opened workspaces page: %d", code)
	}
	if _, body, _ := post(t, client, e.srv.URL+"/settings/users", url.Values{"email": {"x@client-a.example"}, "role": {"org-admin"}}); !strings.Contains(body, "allowed to give") {
		t.Fatal("client admin made an organization admin")
	}
	// Tokens and agents the client creates belong to Client A.
	post(t, client, e.srv.URL+"/agents", url.Values{"name": {"client-agent"}})
	if _, err := e.st.GetAgentByName(ctx, clientWS.Scope(), "client-agent"); err != nil {
		t.Fatal("agent not created in the client workspace")
	}
	if _, err := e.st.GetAgentByName(ctx, e.ws.Scope(), "client-agent"); err == nil {
		t.Fatal("agent leaked into the MSP workspace")
	}

	// Taking access away works and locks the client out.
	u, _ := e.st.GetUserByEmail(ctx, e.ws.OrgID, "ops@client-a.example")
	post(t, admin, e.srv.URL+"/settings/users/"+u.ID+"/role", url.Values{"role": {"none"}})
	if code, _ := get(t, client, e.srv.URL+"/", nil); code != http.StatusForbidden {
		t.Fatalf("client without access: %d", code)
	}
}

// addInbox adds unmapped workloads to the env's target: workload name -> image.
func (e *uiEnv) addInbox(workloads map[string]string) {
	e.t.Helper()
	ctx := context.Background()
	sc := e.ws.Scope()
	existing, _ := e.st.ListTargetInstances(ctx, sc, e.tgt.ID)
	ch := store.SnapshotChanges{Scope: sc, SnapshotID: store.NewID(), TargetID: e.tgt.ID, At: time.Now(), Upsert: existing}
	for name, image := range workloads {
		ref := versions.ParseImage(image)
		ch.Upsert = append(ch.Upsert, store.Instance{
			TargetID: e.tgt.ID, EnvironmentID: e.prod.ID, WorkloadID: "id-" + name, WorkloadKind: "deployment", WorkloadName: name,
			ContainerName: "app", Image: image, Tag: ref.Tag, Running: 1, IsMain: true, SuggestedService: ref.Name(),
		})
	}
	_, _ = e.st.InsertSnapshot(ctx, store.Snapshot{ID: ch.SnapshotID, Scope: sc, TargetID: e.tgt.ID, CollectedAt: time.Now(), Complete: true, Payload: json.RawMessage(`{}`)})
	if err := e.st.ApplySnapshot(ctx, ch); err != nil {
		e.t.Fatal(err)
	}
}

func (e *uiEnv) serviceOf(workload string) string {
	e.t.Helper()
	active, _ := e.st.ListActiveInstances(context.Background(), e.ws.Scope())
	for _, i := range active {
		if i.WorkloadName == workload && i.ServiceID != "" {
			svc, _ := e.st.GetService(context.Background(), e.ws.Scope(), i.ServiceID)
			return svc.Name
		}
	}
	return ""
}

func TestInboxGroupsByImage(t *testing.T) {
	e := newUIEnv(t)
	c := e.as(store.RoleMember)
	e.addInbox(map[string]string{
		"cache-a": "redis:7.2.5", "cache-b": "redis:7.4.0", "cache-c": "redis:7.4.0",
		"api": "ghcr.io/acme/app:3.1.0", "jobs": "ghcr.io/acme/app:3.1.0",
		"debug": "busybox:1.36",
	})

	_, body := get(t, c, e.srv.URL+"/inbox", nil)
	if !strings.Contains(body, "Map all 3") || !strings.Contains(body, "7.2.5, 7.4.0") || !strings.Contains(body, "Map only this") {
		t.Fatalf("inbox not grouped: %s", body)
	}
	if strings.Index(body, "docker.io/library/redis") > strings.Index(body, "docker.io/library/busybox") {
		t.Fatal("larger groups come first")
	}

	// One image, two services: map one workload on its own, then the rest of the image.
	_, body, _ = post(t, c, e.srv.URL+"/inbox/map", url.Values{
		"service": {"jobs"}, "repo": {"ghcr.io/acme/app"}, "only": {"workload"}, "workload_name": {"jobs"},
	})
	if !strings.Contains(body, "Mapped jobs to jobs") || e.serviceOf("jobs") != "jobs" || e.serviceOf("api") != "" {
		t.Fatalf("map only this: jobs=%q api=%q", e.serviceOf("jobs"), e.serviceOf("api"))
	}
	_, body, _ = post(t, c, e.srv.URL+"/inbox/map", url.Values{"service": {"app"}, "repo": {"ghcr.io/acme/app"}})
	if !strings.Contains(body, "Mapped 1 workload to app") || e.serviceOf("api") != "app" || e.serviceOf("jobs") != "jobs" {
		t.Fatalf("map rest: api=%q jobs=%q", e.serviceOf("api"), e.serviceOf("jobs"))
	}

	_, body, _ = post(t, c, e.srv.URL+"/inbox/map", url.Values{"service": {"redis"}, "repo": {"docker.io/library/redis"}})
	if !strings.Contains(body, "Mapped 3 workloads to redis") {
		t.Fatalf("map all: %s", body)
	}
	for _, w := range []string{"cache-a", "cache-b", "cache-c"} {
		if e.serviceOf(w) != "redis" {
			t.Fatalf("%s not mapped at once", w)
		}
	}

	// The workload rule wins over the image rule for later snapshots too.
	rules, _ := e.st.ListMappingRules(context.Background(), e.ws.Scope())
	m, _ := mapping.New(rules)
	if d := m.Map(mapping.Workload{Name: "jobs"}, "app", versions.ParseImage("ghcr.io/acme/app:3.2.0")); d.ServiceID == "" || d.ServiceID == m.Map(mapping.Workload{Name: "api"}, "app", versions.ParseImage("ghcr.io/acme/app:3.2.0")).ServiceID {
		t.Fatalf("rules: %+v", rules)
	}

	_, body, _ = post(t, c, e.srv.URL+"/inbox/ignore", url.Values{"repo": {"docker.io/library/busybox"}})
	if strings.Contains(body, "debug") || !strings.Contains(body, "busybox is ignored") {
		t.Fatal("ignored image still in the inbox")
	}
}

func TestOrganizationRoles(t *testing.T) {
	e := newUIEnv(t)
	ctx := context.Background()
	owner := e.as(store.RoleOwner)
	admin := e.as(store.RoleAdmin)
	dev, _ := e.st.CreateUser(ctx, e.ws.OrgID, "dev@example.com", "", store.RoleMember)
	_ = e.st.SetMembership(ctx, dev.ID, e.ws.ID, store.RoleMember)
	roleOf := func(id string) string {
		u, _ := e.st.GetUser(ctx, id)
		return u.Role
	}
	setRole := func(c *http.Client, id, role string) string {
		_, body, _ := post(t, c, e.srv.URL+"/settings/users/"+id+"/role", url.Values{"role": {role}})
		return body
	}

	// An organization admin promotes a member to admin of the organization, not to owner.
	if body := setRole(admin, dev.ID, "org-admin"); !strings.Contains(body, "is now admin of the organization") || roleOf(dev.ID) != store.RoleAdmin {
		t.Fatalf("promote: %s", roleOf(dev.ID))
	}
	if body := setRole(admin, dev.ID, "owner"); !strings.Contains(body, "allowed to give") || roleOf(dev.ID) != store.RoleAdmin {
		t.Fatal("admin made an owner")
	}
	if _, body := get(t, admin, e.srv.URL+"/settings", nil); !strings.Contains(body, `value="org-admin" selected`) {
		t.Fatal("org role not shown as selected")
	}

	// Back to workspace access: the membership in this workspace decides again.
	if body := setRole(admin, dev.ID, "viewer"); !strings.Contains(body, "is now viewer") || roleOf(dev.ID) != store.RoleViewer {
		t.Fatalf("demote: %s", roleOf(dev.ID))
	}
	if roles, _ := e.st.WorkspaceRoles(ctx, e.ws.ID); roles[dev.ID] != store.RoleViewer {
		t.Fatalf("membership %q", roles[dev.ID])
	}

	// Owners: only owners change them, and the last one stays.
	users, _ := e.st.ListUsers(ctx, e.ws.OrgID)
	var ownerID string
	for _, u := range users {
		if u.Role == store.RoleOwner {
			ownerID = u.ID
		}
	}
	if body := setRole(admin, ownerID, "org-admin"); !strings.Contains(body, "allowed to give") || roleOf(ownerID) != store.RoleOwner {
		t.Fatal("admin demoted an owner")
	}
	if body := setRole(owner, dev.ID, "owner"); !strings.Contains(body, "is now owner") {
		t.Fatalf("owner promotes: %s", roleOf(dev.ID))
	}
	if body := setRole(owner, dev.ID, "member"); !strings.Contains(body, "is now member") || roleOf(dev.ID) != store.RoleMember {
		t.Fatalf("owner demotes another owner: %s", roleOf(dev.ID))
	}
	// Now ownerID is the only owner; nobody else can demote or remove them.
	if _, body, _ := post(t, admin, e.srv.URL+"/settings/users/"+ownerID+"/delete", nil); !strings.Contains(body, "Only owners") {
		t.Fatal("admin removed the owner")
	}
}

func TestAccountPasswordAndSessions(t *testing.T) {
	e := newUIEnv(t)
	ctx := context.Background()
	viaLink := e.as(store.RoleViewer) // signed in with a link just now
	other := e.as(store.RoleMember)

	_, body := get(t, viaLink, e.srv.URL+"/account", nil)
	if !strings.Contains(body, "Set a password") || strings.Contains(body, `name="current"`) || !strings.Contains(body, "this device") {
		t.Fatalf("account page: %s", body)
	}

	// Right after a link sign-in, a password is set without the current one.
	_, body, _ = post(t, viaLink, e.srv.URL+"/account/password", url.Values{"password": {"short"}, "confirm": {"short"}})
	if !strings.Contains(body, "Choose another password: use at least 12 characters.") {
		t.Fatalf("weak password: %s", body)
	}
	_, body, _ = post(t, viaLink, e.srv.URL+"/account/password", url.Values{"password": {"correct horse staple"}, "confirm": {"correct horse stapler"}})
	if !strings.Contains(body, "The two passwords differ.") {
		t.Fatalf("mismatch: %s", body)
	}
	_, body, _ = post(t, viaLink, e.srv.URL+"/account/password", url.Values{"password": {"correct horse staple"}, "confirm": {"correct horse staple"}})
	if !strings.Contains(body, "Password saved.") {
		t.Fatalf("set: %s", body)
	}

	// Signing in with the password; that session must prove the current password.
	jar, _ := cookiejar.New(nil)
	pw := &http.Client{Jar: jar}
	if _, body, _ := post(t, pw, e.srv.URL+"/auth/password", url.Values{"email": {"viewer@example.com"}, "password": {"correct horse staple"}}); strings.Contains(body, "Sign in") && !strings.Contains(body, "Sign out") {
		t.Fatalf("password sign-in: %s", body)
	}
	_, body = get(t, pw, e.srv.URL+"/account", nil)
	if !strings.Contains(body, `name="current"`) || !strings.Contains(body, "Change password") || !strings.Contains(body, "Sign out other devices") {
		t.Fatalf("account after password sign-in: %s", body)
	}
	_, body, _ = post(t, pw, e.srv.URL+"/account/password", url.Values{"current": {"guess"}, "password": {"another long secret"}, "confirm": {"another long secret"}})
	if !strings.Contains(body, "The current password is wrong.") {
		t.Fatalf("wrong current: %s", body)
	}
	_, body, _ = post(t, pw, e.srv.URL+"/account/password", url.Values{
		"current": {"correct horse staple"}, "password": {"another long secret"}, "confirm": {"another long secret"},
	})
	if !strings.Contains(body, "Password saved.") {
		t.Fatalf("change: %s", body)
	}
	// The change signed out the link session, not this one, and not other people.
	if _, b := get(t, viaLink, e.srv.URL+"/account", nil); strings.Contains(b, "Your account") {
		t.Fatal("other device still signed in after a password change")
	}
	if _, b := get(t, other, e.srv.URL+"/account", nil); !strings.Contains(b, "Your account") {
		t.Fatal("someone else was signed out")
	}

	// Names, and API tokens have no account.
	_, body, _ = post(t, pw, e.srv.URL+"/account/name", url.Values{"name": {"Viktor"}})
	if !strings.Contains(body, `value="Viktor"`) {
		t.Fatalf("name: %s", body)
	}
	raw, hash := tokens.New(tokens.API)
	if _, err := e.st.CreateAPIToken(ctx, e.ws.Scope(), store.APIToken{Name: "t", Role: store.RoleMember}, hash); err != nil {
		t.Fatal(err)
	}
	noFollow := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	if code, _ := get(t, noFollow, e.srv.URL+"/account", map[string]string{"Authorization": "Bearer " + raw}); code != http.StatusSeeOther {
		t.Fatal("API token opened the account page")
	}

	// Admins see who has a password and can take it away.
	admin := e.as(store.RoleAdmin)
	_, body = get(t, admin, e.srv.URL+"/settings", nil)
	if !strings.Contains(body, "Remove password") {
		t.Fatalf("users page: %s", body)
	}
	u, _ := e.st.GetUserByEmail(ctx, e.ws.OrgID, "viewer@example.com")
	_, body, _ = post(t, admin, e.srv.URL+"/settings/users/"+u.ID+"/password/delete", nil)
	if !strings.Contains(body, "Password of viewer@example.com removed") {
		t.Fatalf("remove: %s", body)
	}
	if _, b := get(t, pw, e.srv.URL+"/account", nil); strings.Contains(b, "Your account") {
		t.Fatal("still signed in after the admin removed the password")
	}
}

func TestAgentPage(t *testing.T) {
	e := newUIEnv(t)
	ctx := context.Background()
	c := e.as(store.RoleAdmin)
	_, body, _ := post(t, c, e.srv.URL+"/agents", url.Values{"name": {"fresh"}})
	if !strings.Contains(body, "Start the agent") || !strings.Contains(body, "helm upgrade --install goliash-agent") ||
		!strings.Contains(body, "GOLIASH_SERVER_URL="+e.srv.URL) {
		t.Fatalf("install snippets: %s", body)
	}
	a, _ := e.st.CreateAgent(ctx, e.ws.Scope(), "eu", "eu-hash")
	us, _ := e.st.CreateAgent(ctx, e.ws.Scope(), "us", "us-hash")
	tgt, _ := e.st.CreateTarget(ctx, store.Target{Scope: e.ws.Scope(), EnvironmentID: e.prod.ID, AgentID: a.ID, Platform: "swarm", Name: "swarm-eu"})
	page := e.srv.URL + "/agents/" + a.ID

	// The agent connects, then gets a new token: the old one keeps working.
	if _, err := e.st.AgentByTokenHash(ctx, "eu-hash"); err != nil {
		t.Fatal(err)
	}
	_, body, hdr := post(t, c, page+"/rotate", nil)
	if !strings.Contains(body, "Give the agent its new token") || !strings.Contains(body, "waiting for the agent to use it") ||
		hdr.Get("Cache-Control") != "no-store" {
		t.Fatalf("rotate: %s", body)
	}

	// Viewers see the page without actions.
	viewer := e.as(store.RoleViewer)
	if _, b := get(t, viewer, page, nil); !strings.Contains(b, "swarm-eu") || strings.Contains(b, "Rotate token") {
		t.Fatal("viewer page")
	}
	if code, _, _ := post(t, viewer, page+"/revoke", nil); code != http.StatusForbidden {
		t.Fatalf("viewer revoked: %d", code)
	}

	if _, body, _ = post(t, c, page+"/rename", url.Values{"name": {"us"}}); !strings.Contains(body, "Another agent is named us.") {
		t.Fatal("rename onto a taken name")
	}
	if _, body, _ = post(t, c, page+"/rename", url.Values{"name": {"eu-1"}}); !strings.Contains(body, "Renamed to eu-1.") {
		t.Fatal("rename")
	}
	if _, body, _ = post(t, c, page+"/delete", nil); !strings.Contains(body, "Move or delete the targets of eu-1 first.") {
		t.Fatal("deleted an agent with targets")
	}
	if _, body, _ = post(t, c, e.srv.URL+"/targets/"+tgt.ID+"/agent", url.Values{"agent": {us.ID}}); !strings.Contains(body, "swarm-eu is collected by us") {
		t.Fatalf("move: %s", body)
	}
	if _, body, _ = post(t, c, page+"/revoke", nil); !strings.Contains(body, "revoked") || !strings.Contains(body, `badge revoked`) {
		t.Fatalf("revoke: %s", body)
	}
	if _, body, _ = post(t, c, page+"/delete", nil); !strings.Contains(body, "Agent eu-1 deleted.") {
		t.Fatalf("delete: %s", body)
	}
	if _, body, _ = post(t, c, e.srv.URL+"/targets/"+tgt.ID+"/delete", nil); !strings.Contains(body, "Target swarm-eu deleted.") {
		t.Fatalf("delete target: %s", body)
	}
	_, page2 := get(t, c, e.srv.URL+"/settings", nil)
	for _, want := range []string{"agent.rotate_token", "agent.rename", "target.move", "agent.revoke", "agent.delete", "target.delete"} {
		if !strings.Contains(page2, want) {
			t.Errorf("audit log misses %s", want)
		}
	}
}
