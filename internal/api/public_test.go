// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/pipozzz/goliash/internal/auth"
	"github.com/pipozzz/goliash/internal/store"
	"github.com/pipozzz/goliash/internal/tokens"
)

type publicEnv struct {
	t      *testing.T
	st     *store.Store
	sc     store.Scope
	srv    *httptest.Server
	token  string
	viewer *http.Client
}

func newPublicEnv(t *testing.T) *publicEnv {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, "sqlite://:memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ws, _ := st.EnsureDefaultWorkspace(ctx)
	sc := ws.Scope()
	stg, _ := st.CreateEnvironment(ctx, sc, "staging", 20)
	prod, _ := st.CreateEnvironment(ctx, sc, "prod", 30)
	tgS, _ := st.CreateTarget(ctx, store.Target{Scope: sc, EnvironmentID: stg.ID, Platform: "swarm", Name: "swarm-stg"})
	tgP, _ := st.CreateTarget(ctx, store.Target{Scope: sc, EnvironmentID: prod.ID, Platform: "kubernetes", Name: "k8s-prod"})
	web, _ := st.EnsureService(ctx, sc, "web")

	for _, x := range []struct {
		target store.Target
		tag    string
	}{{tgS, "1.27.3"}, {tgP, "1.27.2"}} {
		snap := store.NewID()
		_, _ = st.InsertSnapshot(ctx, store.Snapshot{ID: snap, Scope: sc, TargetID: x.target.ID, CollectedAt: time.Now(), Complete: true, Payload: json.RawMessage(`{}`)})
		if err := st.ApplySnapshot(ctx, store.SnapshotChanges{
			Scope: sc, SnapshotID: snap, TargetID: x.target.ID, At: time.Now(),
			Upsert: []store.Instance{{
				TargetID: x.target.ID, EnvironmentID: x.target.EnvironmentID, ServiceID: web.ID, WorkloadID: "w",
				WorkloadKind: "deployment", WorkloadName: "web", ContainerName: "nginx", Image: "nginx:" + x.tag, Tag: x.tag, Running: 2, IsMain: true,
			}},
			Events: []store.Event{{Type: "deployed", ServiceID: web.ID, EnvironmentID: x.target.EnvironmentID, TargetID: x.target.ID, ToVersion: x.tag, At: time.Now()}},
		}); err != nil {
			t.Fatal(err)
		}
	}
	_, _ = st.InsertReleases(ctx, sc, web.ID, []string{"1.27.2", "1.27.3", "1.28.0"})
	_, _ = st.OpenDrift(ctx, store.Drift{
		Scope: sc, ServiceID: web.ID, EnvironmentID: prod.ID, Kind: "env",
		Detail: json.RawMessage(`{"running":"1.27.2","other":"1.27.3","other_in":"staging"}`), Since: time.Now().Add(-48 * time.Hour),
	})

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	a, err := auth.New(st, log, srv.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	a.Routes(mux)
	NewPublicHandler(st, a, log).Register(mux)

	token, hash := tokens.New(tokens.API)
	_, _ = st.CreateAPIToken(ctx, sc, store.APIToken{Name: "test", Role: store.RoleMember}, hash)

	// A viewer signed in through a magic link.
	viewer, _ := st.CreateUser(ctx, ws.OrgID, "viewer@example.com", "", store.RoleViewer)
	_ = st.SetMembership(ctx, viewer.ID, ws.ID, store.RoleViewer)
	link, _ := a.LoginLink(ctx, viewer)
	jarClient := &http.Client{Jar: newJar()}
	resp, err := jarClient.Get(link) //nolint:noctx // test
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	return &publicEnv{t: t, st: st, sc: sc, srv: srv, token: token, viewer: jarClient}
}

func (e *publicEnv) call(c *http.Client, method, path, token, body string) (int, string) {
	e.t.Helper()
	req, _ := http.NewRequestWithContext(context.Background(), method, e.srv.URL+path, strings.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if c == nil {
		c = http.DefaultClient
	}
	resp, err := c.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func TestPublicAPIAuth(t *testing.T) {
	e := newPublicEnv(t)
	if code, _ := e.call(nil, "GET", "/api/v1/matrix", "", ""); code != http.StatusUnauthorized {
		t.Fatalf("anonymous matrix: %d", code)
	}
	if code, _ := e.call(e.viewer, "GET", "/api/v1/matrix", "", ""); code != http.StatusOK {
		t.Fatalf("viewer matrix: %d", code)
	}
	if code, body := e.call(e.viewer, "POST", "/api/v1/acks", "", `{"service":"web","kind":"release","until_version":"2.0.0"}`); code != http.StatusForbidden {
		t.Fatalf("viewer ack: %d %s", code, body)
	}
	if code, body := e.call(nil, "POST", "/api/v1/acks", e.token, `{"service":"web","kind":"release","until_version":"2.0.0"}`); code != http.StatusCreated {
		t.Fatalf("token ack: %d %s", code, body)
	}
	if acks, _ := e.st.ListAcks(context.Background(), e.sc); len(acks) != 1 || acks[0].CreatedBy != "api token test" {
		t.Fatalf("acks %+v", acks)
	}
	// A viewer token reads but does not acknowledge.
	ro, roHash := tokens.New(tokens.API)
	_, _ = e.st.CreateAPIToken(context.Background(), e.sc, store.APIToken{Name: "grafana", Role: store.RoleViewer}, roHash)
	if code, _ := e.call(nil, "GET", "/api/v1/matrix", ro, ""); code != http.StatusOK {
		t.Fatalf("viewer token matrix: %d", code)
	}
	if code, _ := e.call(nil, "POST", "/api/v1/acks", ro, `{"service":"web","kind":"release","until_version":"2.0.0"}`); code != http.StatusForbidden {
		t.Fatalf("viewer token ack: %d", code)
	}
	if code, _ := e.call(nil, "POST", "/api/v1/acks", e.token, `{"service":"web","kind":"oops","until_version":"2"}`); code != http.StatusBadRequest {
		t.Fatal("bad kind accepted")
	}
	if code, _ := e.call(nil, "GET", "/api/v1/nope", e.token, ""); code != http.StatusNotFound {
		t.Fatal("unknown endpoint")
	}
}

func TestPublicAPIMatrixEventsDrifts(t *testing.T) {
	e := newPublicEnv(t)
	_, body := e.call(nil, "GET", "/api/v1/matrix", e.token, "")
	var m apiMatrix
	if err := json.Unmarshal([]byte(body), &m); err != nil {
		t.Fatal(err)
	}
	if len(m.Environments) != 2 || len(m.Services) != 1 {
		t.Fatalf("matrix %s", body)
	}
	web := m.Services[0]
	if web.Service != "web" || web.Latest != "1.28.0" || web.Upstream != "docker.io/library/nginx" ||
		web.Cells[0].Versions[0].Tag != "1.27.3" || web.Cells[1].Versions[0].Targets[0] != "k8s-prod" ||
		len(web.Cells[1].Drifts) != 1 || web.Cells[1].Drifts[0].DaysOpen < 1.9 || web.Cells[1].Drifts[0].Detail.OtherIn != "staging" {
		t.Fatalf("web row %s", body)
	}

	_, body = e.call(nil, "GET", "/api/v1/events?environment=prod&type=deployed", e.token, "")
	var evs []map[string]any
	_ = json.Unmarshal([]byte(body), &evs)
	if len(evs) != 1 || evs[0]["to"] != "1.27.2" || evs[0]["target"] != "k8s-prod" || evs[0]["service"] != "web" {
		t.Fatalf("events %s", body)
	}
	if code, _ := e.call(nil, "GET", "/api/v1/events?service=missing", e.token, ""); code != http.StatusNotFound {
		t.Fatal("unknown service filter")
	}
	if code, _ := e.call(nil, "GET", "/api/v1/events?before=yesterday", e.token, ""); code != http.StatusBadRequest {
		t.Fatal("bad before accepted")
	}

	_, body = e.call(nil, "GET", "/api/v1/drifts", e.token, "")
	if !strings.Contains(body, `"kind":"env"`) || !strings.Contains(body, `"environment":"prod"`) {
		t.Fatalf("drifts %s", body)
	}
	for _, path := range []string{"/api/v1/services", "/api/v1/environments", "/api/v1/targets"} {
		if code, body := e.call(nil, "GET", path, e.token, ""); code != 200 || !strings.HasPrefix(body, "[") {
			t.Fatalf("%s: %d %s", path, code, body)
		}
	}
}

func TestMetrics(t *testing.T) {
	e := newPublicEnv(t)
	code, body := e.call(nil, "GET", "/metrics", e.token, "")
	if code != 200 {
		t.Fatalf("metrics %d", code)
	}
	for _, want := range []string{
		`goliash_deployed_version_info{service="web",environment="prod",version="1.27.2"} 2`,
		`goliash_deployed_version_info{service="web",environment="staging",version="1.27.3"} 2`,
		`goliash_outdated{service="web",environment="prod"} 0`,
		`goliash_build_info{version="dev"} 1`,
		`goliash_snapshots_pending 0`,
		`goliash_notifications_queued 0`,
		`goliash_agents{status="revoked"} 0`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %s\n%s", want, body)
		}
	}
	if !regexp.MustCompile(`goliash_drift_days\{service="web",environment="prod",kind="env"\} 2\.\d\d`).MatchString(body) {
		t.Errorf("drift days missing\n%s", body)
	}
	// Each metric family's samples follow its own TYPE line.
	family := ""
	for _, line := range strings.Split(strings.TrimSpace(body), "\n") {
		if strings.HasPrefix(line, "# TYPE ") {
			family = strings.Fields(line)[2]
			continue
		}
		if !strings.HasPrefix(line, "#") && !strings.HasPrefix(line, family+"{") && !strings.HasPrefix(line, family+" ") {
			t.Errorf("sample %q outside its family %s", line, family)
		}
	}
	if code, _ := e.call(nil, "GET", "/metrics", "", ""); code != http.StatusUnauthorized {
		t.Fatal("metrics without auth")
	}
}

// The Grafana and SigNoz dashboards in deploy/ read only metrics /metrics serves, and they show every family
// except goliash_notifications_queued (a queue that is often legitimately non-zero).
func TestDashboardsUseServedMetrics(t *testing.T) {
	src, err := os.ReadFile("public.go")
	if err != nil {
		t.Fatal(err)
	}
	served := map[string]bool{}
	for _, m := range regexp.MustCompile(`# HELP (goliash_\w+)`).FindAllStringSubmatch(string(src), -1) {
		served[m[1]] = true
	}
	for _, f := range []string{"../../deploy/grafana/goliash-dashboard.json", "../../deploy/signoz/goliash-dashboard.json"} {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if !json.Valid(b) {
			t.Fatalf("%s is not JSON", f)
		}
		used := map[string]bool{}
		for _, m := range regexp.MustCompile(`goliash_[a-z_]+`).FindAllString(string(b), -1) {
			used[m] = true
		}
		for m := range used {
			if !served[m] {
				t.Errorf("%s reads %s, which /metrics does not serve", f, m)
			}
		}
		for m := range served {
			if !used[m] && m != "goliash_notifications_queued" {
				t.Errorf("%s leaves out %s", f, m)
			}
		}
	}
	rules, err := os.ReadFile("../../deploy/prometheus/goliash-alerts.yaml")
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range regexp.MustCompile(`goliash_[a-z_]+`).FindAllString(string(rules), -1) {
		if !served[m] {
			t.Errorf("the alerts read %s, which /metrics does not serve", m)
		}
	}
	// The Helm chart carries copies, since a chart cannot read files outside its directory.
	for chart, orig := range map[string]string{
		"../../deploy/helm/goliash/files/goliash-alerts.yaml":    "../../deploy/prometheus/goliash-alerts.yaml",
		"../../deploy/helm/goliash/files/goliash-dashboard.json": "../../deploy/grafana/goliash-dashboard.json",
	} {
		a, _ := os.ReadFile(chart)
		b, _ := os.ReadFile(orig)
		if len(a) == 0 || string(a) != string(b) {
			t.Errorf("%s differs from %s: copy it again", chart, orig)
		}
	}
}

func newJar() http.CookieJar {
	jar, _ := cookiejar.New(nil)
	return jar
}

func TestParseSince(t *testing.T) {
	now := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	for in, want := range map[string]time.Time{
		"2h": now.Add(-2 * time.Hour), "30m": now.Add(-30 * time.Minute), "7d": now.Add(-7 * 24 * time.Hour),
		"2026-10-01T00:00:00Z": time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC),
	} {
		if got, err := parseSince(in, now); err != nil || !got.Equal(want) {
			t.Errorf("parseSince(%q) = %v %v", in, got, err)
		}
	}
	for _, bad := range []string{"yesterday", "-2h", "xd"} {
		if _, err := parseSince(bad, now); err == nil {
			t.Errorf("parseSince(%q) accepted", bad)
		}
	}
}

func TestVulnerabilitiesAPI(t *testing.T) {
	e := newPublicEnv(t)
	ctx := context.Background()
	prod, _ := e.st.GetEnvironmentByName(ctx, e.sc, "prod")
	targets, _ := e.st.ListTargets(ctx, e.sc)
	for _, tg := range targets {
		if tg.EnvironmentID != prod.ID {
			continue
		}
		insts, _ := e.st.ListTargetInstances(ctx, e.sc, tg.ID)
		var remove []string
		for i := range insts {
			remove = append(remove, insts[i].ID)
			insts[i].ID, insts[i].Digest = "", "sha256:"+strings.Repeat("a", 64) // a new digest is a new instance
		}
		if err := e.st.ApplySnapshot(ctx, store.SnapshotChanges{Scope: e.sc, SnapshotID: store.NewID(), TargetID: tg.ID, At: time.Now(), Upsert: insts, Remove: remove}); err != nil {
			t.Fatal(err)
		}
	}
	purl := "pkg:maven/org.apache.logging.log4j/log4j-core@2.14.1"
	_ = e.st.SetImageSBOM(ctx, e.sc, store.ImageSBOM{Repo: "docker.io/library/nginx", Digest: "sha256:" + strings.Repeat("a", 64), Purls: []string{purl}})
	_ = e.st.SetPackageVulns(ctx, map[string][]string{purl: {"GHSA-jfh8-c2jp-5v3q"}})
	_ = e.st.SetVuln(ctx, store.Vuln{
		ID: "GHSA-jfh8-c2jp-5v3q", Aliases: []string{"CVE-2021-44228"}, Severity: "CRITICAL",
		Fixes: map[string][]string{"Maven|org.apache.logging.log4j:log4j-core": {"2.15.0"}},
	})
	_ = e.st.SetKnownExploited(ctx, []store.Exploited{{CVE: "CVE-2021-44228", DueDate: "2021-12-24", Ransomware: true}})

	var got struct {
		Complete bool `json:"complete"`
		Findings []struct {
			ID        string `json:"id"`
			Exploited *struct {
				Ransomware bool `json:"ransomware"`
			} `json:"exploited"`
			Affected []struct {
				Service, Environment, Package, Fix string
			} `json:"affected"`
		} `json:"findings"`
	}
	for _, q := range []string{"q=CVE-2021-44228", "q=log4j&environment=prod&exploited=true"} {
		code, body := e.call(nil, "GET", "/api/v1/vulnerabilities?"+q, e.token, "")
		if code != 200 || json.Unmarshal([]byte(body), &got) != nil {
			t.Fatalf("%s: %d %s", q, code, body)
		}
		if !got.Complete || len(got.Findings) != 1 || got.Findings[0].ID != "CVE-2021-44228" || got.Findings[0].Exploited == nil ||
			!got.Findings[0].Exploited.Ransomware || got.Findings[0].Affected[0].Fix != "2.15.0" || got.Findings[0].Affected[0].Environment != "prod" {
			t.Fatalf("%s: %s", q, body)
		}
	}
	if _, body := e.call(nil, "GET", "/api/v1/vulnerabilities?q=CVE-2024-3094", e.token, ""); !strings.Contains(body, `"findings":[]`) || !strings.Contains(body, `"complete":true`) {
		t.Fatalf("not running: %s", body)
	}
}
