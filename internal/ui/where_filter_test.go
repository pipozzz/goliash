// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package ui

import (
	"context"
	"encoding/json"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/pipozzz/goliash/internal/store"
	"github.com/pipozzz/goliash/internal/versions"
)

// Where an agent runs links to the matrix only when that finds something, and search finds by it.
func TestWhereLinksAndSearch(t *testing.T) {
	e := newUIEnv(t)
	ctx := t.Context()
	c := e.as(store.RoleAdmin)
	where := map[string]string{"cloud": "aws", "account": "098225058563", "region": "eu-central-1", "os": "Ubuntu 26.04 LTS"}
	a, _ := e.st.CreateAgent(ctx, e.ws.Scope(), "eu", "eu-hash")
	tgts, _ := e.st.ListTargets(ctx, e.ws.Scope())
	_ = e.st.SetTargetAgent(ctx, e.ws.Scope(), tgts[0].ID, a.ID)
	_ = e.st.SetAgentWhere(ctx, e.ws.Scope(), a.ID, where)
	idle, _ := e.st.CreateAgent(ctx, e.ws.Scope(), "idle", "idle-hash")
	_ = e.st.SetAgentWhere(ctx, e.ws.Scope(), idle.ID, map[string]string{"cloud": "aws", "zone": "us-east-1b"})

	if _, page := get(t, c, e.srv.URL+"/agents/"+a.ID, nil); !strings.Contains(page, `q=account%3A098225058563`) ||
		!strings.Contains(page, `q=os%3Aubuntu`) || !strings.Contains(page, "1 service") {
		t.Fatal("the agent's where does not link to what runs there")
	}
	if _, page := get(t, c, e.srv.URL+"/agents/"+idle.ID, nil); strings.Contains(page, `q=zone%3A`) || !strings.Contains(page, "us-east-1b") {
		t.Fatal("a where that finds nothing is a link")
	}

	_, page := get(t, c, e.srv.URL+"/search?q=eu-central-1", nil)
	for _, want := range []string{"in region:eu-central-1", "Agents and targets", `href="/agents/` + a.ID + `"`, "k8s-prod"} {
		if !strings.Contains(page, want) {
			t.Errorf("search for a region misses %q", want)
		}
	}
	if _, page := get(t, c, e.srv.URL+"/search?q=us-east-1b", nil); !strings.Contains(page, `href="/agents/`+idle.ID+`"`) {
		t.Error("search does not find an agent by its zone")
	}
}

// A target's page says where it runs, each a filter of the matrix.
func TestTargetPageSaysWhere(t *testing.T) {
	e := newUIEnv(t)
	ctx := t.Context()
	a, _ := e.st.CreateAgent(ctx, e.ws.Scope(), "eu", "eu-hash")
	tgts, _ := e.st.ListTargets(ctx, e.ws.Scope())
	_ = e.st.SetTargetAgent(ctx, e.ws.Scope(), tgts[0].ID, a.ID)
	_ = e.st.SetAgentWhere(ctx, e.ws.Scope(), a.ID, map[string]string{"cloud": "aws", "region": "eu-central-1"})
	_, page := get(t, e.as(store.RoleViewer), e.srv.URL+"/targets/"+tgts[0].ID, nil)
	if !strings.Contains(page, `q=region%3Aeu-central-1`) || strings.Contains(page, `q=platform%3A`) {
		t.Fatal("the target page does not say where it runs")
	}
}

// The agents page filters agents and targets by where they run; an API target keeps its own region.
func TestAgentsPageFilterWords(t *testing.T) {
	e := newUIEnv(t)
	ctx := t.Context()
	a, _ := e.st.CreateAgent(ctx, e.ws.Scope(), "eu", "eu-hash")
	_ = e.st.SetAgentWhere(ctx, e.ws.Scope(), a.ID, map[string]string{"cloud": "aws", "account": "098225058563", "region": "eu-central-1", "os": "Ubuntu 26.04 LTS"})
	_, _ = e.st.CreateTarget(ctx, store.Target{
		Scope: e.ws.Scope(), EnvironmentID: e.prod.ID, AgentID: a.ID, Platform: "ecs", Name: "ecs-us",
		Settings: []byte(`{"ecs":{"region":"us-east-1"}}`),
	})
	_, page := get(t, e.as(store.RoleViewer), e.srv.URL+"/agents", nil)
	for _, want := range []string{
		`id="page-filter"`,
		`data-search="eu never  `, // name, status, no version
		"os:ubuntu 26.04 lts",
		`data-search="ecs-us ecs prod eu`,
		"region:us-east-1",
		"098225058563 · us-east-1",
	} {
		if !strings.Contains(page, want) {
			t.Errorf("agents page misses %q", want)
		}
	}
	if strings.Contains(page, `data-search="ecs-us ecs prod eu  cloud:aws account:098225058563 region:us-east-1 os:`) {
		t.Error("an API target took the agent's OS")
	}
}

// Check now reads a service's images and asks OSV again, whatever registry it is on.
func TestCheckNow(t *testing.T) {
	e := newUIEnv(t)
	c := e.as(store.RoleMember)
	svcs, _ := e.st.ListServices(t.Context(), e.ws.Scope())
	name := svcs[0].Name
	if _, page := get(t, c, e.srv.URL+serviceURL(name), nil); !strings.Contains(page, ">Check now</button>") {
		t.Fatal("no Check now")
	}
	if _, body, _ := post(t, c, e.srv.URL+serviceURL(name)+"/check", nil); !strings.Contains(body, "asking OSV") && !strings.Contains(body, "being read already") {
		t.Fatalf("check now: %s", body)
	}
}

// The upstream releases say where each runs.
func TestReleasesSayWhereTheyRun(t *testing.T) {
	e := newUIEnv(t)
	svcs, _ := e.st.ListServices(t.Context(), e.ws.Scope())
	_, _ = e.st.InsertReleases(t.Context(), e.ws.Scope(), svcs[0].ID, []string{"1.27.2", "1.28.0"})
	_, page := get(t, e.as(store.RoleViewer), e.srv.URL+serviceURL(svcs[0].Name), nil)
	if !strings.Contains(page, "runs in prod") {
		t.Fatal("the running release is not marked")
	}
}

func TestStillExploited(t *testing.T) {
	d := versions.VulnDelta{Known: true, Fixed: []versions.VulnRef{{ID: "CVE-1", Exploited: true}}}
	if got := stillExploited([]string{"CVE-1", "CVE-2"}, d); len(got) != 1 || got[0] != "CVE-2" {
		t.Fatalf("still %v", got)
	}
}

// The vulnerabilities list says which versions of a service have the package, so a release built on the old base
// shows as itself.
func TestByPackageVersions(t *testing.T) {
	f := versions.VulnFinding{Affected: []versions.Affected{
		{Service: "leadsystem", Env: "production", Image: "1.dkr.ecr.us-east-2.amazonaws.com/leadsystem:6.0.239", Package: "libbrotli1 1.0.9-2+b6"},
		{Service: "leadsystem", Env: "production", Image: "1.dkr.ecr.us-east-2.amazonaws.com/leadsystem:6.0.240", Package: "libbrotli1 1.0.9-2+b6"},
		{Service: "leadsystem", Env: "production", Image: "1.dkr.ecr.us-east-2.amazonaws.com/leadsystem:6.0.240", Package: "libbrotli1 1.0.9-2+b6"},
	}}
	got := byPackage(f)
	if len(got) != 1 || len(got[0].Where) != 1 || got[0].Where[0].Label() != " 6.0.239, 6.0.240 (production)" {
		t.Fatalf("uses %+v", got)
	}
}

// Searching the vulnerabilities for an image checks it, running or not, and looks again until it is done.
func TestVulnsChecksAnImage(t *testing.T) {
	e := newUIEnv(t)
	_, page := get(t, e.as(store.RoleViewer), e.srv.URL+"/security/vulns?q="+"registry.example.com/acme/api:2.0.0", nil)
	for _, want := range []string{`id="image-check"`, "runs nowhere yet", `hx-trigger="every 10s"`} {
		if !strings.Contains(page, want) {
			t.Errorf("image check misses %q", want)
		}
	}
}

// Many ways in read as one line per kind.
func TestExposedGroups(t *testing.T) {
	ways := []string{
		"load balancer target group bbd-3-psql-ai-admin-tg (internal or not: not known)",
		"load balancer target group bbd-3-psql-ai-client-tg (internal or not: not known)",
		"ingress api.example.com",
		"port 5432/tcp published",
	}
	g := exposedGroups(ways)
	if len(g) != 3 || g[0].Kind != "load balancer target group (internal or not: not known)" || len(g[0].Names) != 2 || g[0].Names[1] != "bbd-3-psql-ai-client-tg" {
		t.Fatalf("groups %+v", g)
	}
	if got := exposedSummary(g); got != "load balancer target group ×2, ingress ×1, other ×1" {
		t.Fatalf("summary %q", got)
	}
}

// A service that runs as several applications on a cluster is a cell each, told apart by its application.
func TestTilesTellAppsApart(t *testing.T) {
	e := newUIEnv(t)
	ctx := t.Context()
	sc := e.ws.Scope()
	tgts, _ := e.st.ListTargets(ctx, sc)
	svc, _ := e.st.EnsureService(ctx, sc, "proxysql")
	snap := store.NewID()
	_, _ = e.st.InsertSnapshot(ctx, store.Snapshot{ID: snap, Scope: sc, TargetID: tgts[0].ID, CollectedAt: time.Now(), Complete: true, Payload: json.RawMessage(`{}`)})
	var ins []store.Instance
	for _, app := range []string{"psql-log", "psql-log-admin"} {
		ins = append(ins, store.Instance{
			TargetID: tgts[0].ID, EnvironmentID: e.prod.ID, ServiceID: svc.ID, WorkloadID: app, WorkloadKind: "service", WorkloadName: app,
			ContainerName: "proxysql", Image: "proxysql/proxysql:2.7.2", Tag: "2.7.2", Running: 1, IsMain: true, App: app, AppSource: "label",
		})
	}
	if err := e.st.ApplySnapshot(ctx, store.SnapshotChanges{Scope: sc, SnapshotID: snap, TargetID: tgts[0].ID, At: time.Now(), Upsert: ins}); err != nil {
		t.Fatal(err)
	}
	_, body := get(t, e.as(store.RoleViewer), e.srv.URL+"/tiles?group=target&app="+tgts[0].Name, nil)
	if !strings.Contains(body, `<span class="board-sub">psql-log</span>`) || !strings.Contains(body, `<span class="board-sub">psql-log-admin</span>`) {
		t.Fatal("the cells of one service do not say their application")
	}
}

// A Docker engine and its host's operating system, collected by one agent, read as one machine: the host right
// under the engine, as its part.
func TestOneMachine(t *testing.T) {
	ts := oneMachine([]TargetView{
		{ID: "h", AgentID: "a1", Name: "ip-1-2", Platform: "host"},
		{ID: "k", AgentID: "a2", Name: "prod", Platform: "kubernetes"},
		{ID: "d", AgentID: "a1", Name: "ip-1", Platform: "docker"},
		{ID: "h3", AgentID: "a3", Name: "vm-3", Platform: "host"}, // a host alone
		{ID: "d4", AgentID: "a4", Name: "tools", Platform: "docker"},
		{ID: "h4", AgentID: "a4", Name: "mail-01", Platform: "host"}, // one agent, two machines
	})
	var order []string
	for _, t := range ts {
		order = append(order, t.ID+":"+t.Machine)
	}
	if strings.Join(order, " ") != "k: d: h:ip-1 h3: d4: h4:" {
		t.Fatalf("order %v", order)
	}
}

// A link to a filtered matrix (a target's region:… chip) opens it filtered: the filter box carries the words, so
// hx-boost swapping the page in does not lose them.
func TestMatrixOpensFiltered(t *testing.T) {
	e := newUIEnv(t)
	_, page := get(t, e.as(store.RoleViewer), e.srv.URL+"/?view=table&q="+url.QueryEscape("region:eu-central-1")+"&drift=1", nil)
	if !strings.Contains(page, `id="matrix-q"`) || !strings.Contains(page, `value="region:eu-central-1"`) {
		t.Fatal("the filter box does not carry ?q=")
	}
	if !strings.Contains(page, `id="matrix-drift" checked`) {
		t.Fatal("the drift box does not carry ?drift=1")
	}
}

// A service page names the targets each version runs on as links to them.
func TestServicePageLinksTargets(t *testing.T) {
	e := newUIEnv(t)
	_, page := get(t, e.as(store.RoleViewer), e.srv.URL+"/services/"+url.PathEscape(e.serviceOf("evil")), nil)
	if !strings.Contains(page, `class="target-link" href="/targets/`+e.tgt.ID+`"`) {
		t.Fatal("the service page does not link the target its version runs on")
	}
}

// A Linux server's page says what it runs, what waits for it and its packages, the vulnerable ones first.
func TestServerPage(t *testing.T) {
	e := newUIEnv(t)
	ctx := context.Background()
	sc := e.ws.Scope()
	host, err := e.st.CreateTarget(ctx, store.Target{Scope: sc, EnvironmentID: e.prod.ID, Platform: "host", Name: "mail-01"})
	if err != nil {
		t.Fatal(err)
	}
	osSvc, _ := e.st.EnsureService(ctx, sc, "ubuntu")
	if err := e.st.ApplySnapshot(ctx, store.SnapshotChanges{Scope: sc, TargetID: host.ID, SnapshotID: store.NewID(), At: time.Now(), Upsert: []store.Instance{{
		TargetID: host.ID, EnvironmentID: e.prod.ID, ServiceID: osSvc.ID, WorkloadID: "os/ubuntu", WorkloadKind: "host_os", WorkloadName: "ubuntu",
		ContainerName: "ubuntu", Image: "docker.io/library/ubuntu:22.04", Tag: "22.04", Running: 1, IsMain: true, CodeDigest: "sha256:h",
		Maintenance: []string{"reboot required: kernel 5.15.0-126-generic installed, 5.15.0-119-generic runs", "package updates: 23 waiting, 9 of them security"},
	}}}); err != nil {
		t.Fatal(err)
	}
	ssl := "pkg:deb/ubuntu/libssl3@3.0.2-0ubuntu1.12?arch=amd64&os_version=22.04&upstream=openssl"
	kernel := "pkg:deb/ubuntu/linux-image-5.15.0-119-generic@5.15.0-119.129?arch=amd64&os_version=22.04&upstream=linux"
	headers := "pkg:deb/ubuntu/linux-headers-5.15.0-119@5.15.0-119.129?arch=all&os_version=22.04&upstream=linux"
	_ = e.st.SetImageSBOM(ctx, sc, store.ImageSBOM{
		Repo: store.LambdaCodeRepo, Digest: "sha256:h", Source: "code",
		Purls: []string{ssl, "pkg:deb/ubuntu/sudo@1.9.9-1ubuntu2.4?arch=amd64&os_version=22.04", kernel, headers},
	})
	_ = e.st.SetPackageVulns(ctx, map[string][]string{
		ssl: {"UBUNTU-CVE-2024-5535"},
		// The kernel's records are not read (gigabytes); the newest kernel no longer has one of its two.
		kernel: {"UBUNTU-CVE-2026-10001", "UBUNTU-CVE-2026-10002"}, versions.NewestKey(kernel): {"UBUNTU-CVE-2026-10002"},
		headers: {"UBUNTU-CVE-2026-10001", "UBUNTU-CVE-2026-10002"},
	})
	_ = e.st.SetVuln(ctx, store.Vuln{
		ID: "UBUNTU-CVE-2024-5535", Aliases: []string{"CVE-2024-5535"}, Severity: "HIGH",
		Fixes: map[string][]string{"Ubuntu:22.04:LTS|openssl": {"3.0.2-0ubuntu1.17"}},
	})
	_, page := get(t, e.as(store.RoleViewer), e.srv.URL+"/targets/"+host.ID, nil)
	for _, want := range []string{
		">Server<", "Ubuntu 22.04", "reboot pending", "23 waiting, 9 of them security", "3 vulnerabilities", "in 4 packages",
		"Packages (4)", ">libssl3<", "3.0.2-0ubuntu1.17", "none known", "operating system",
		">linux-image-5.15.0-119-generic<", "a newer version", "(1 of 2)",
	} {
		if !strings.Contains(page, want) {
			t.Errorf("server page misses %q", want)
		}
	}
	if strings.Contains(page, ">Application<") {
		t.Error("a server's page offers grouping by application")
	}
}

// TestServerExploitedInUpdates: a server's exploited vulnerabilities show on the operating system's line of
// Updates with the server, the package that has them and what fixes it.
func TestServerExploitedInUpdates(t *testing.T) {
	e := newUIEnv(t)
	ctx := context.Background()
	sc := e.ws.Scope()
	host, err := e.st.CreateTarget(ctx, store.Target{Scope: sc, EnvironmentID: e.prod.ID, Platform: "host", Name: "mail-01"})
	if err != nil {
		t.Fatal(err)
	}
	osSvc, _ := e.st.EnsureService(ctx, sc, "ubuntu")
	if err := e.st.ApplySnapshot(ctx, store.SnapshotChanges{Scope: sc, TargetID: host.ID, SnapshotID: store.NewID(), At: time.Now(), Upsert: []store.Instance{{
		TargetID: host.ID, EnvironmentID: e.prod.ID, ServiceID: osSvc.ID, WorkloadID: "os/ubuntu", WorkloadKind: "host_os", WorkloadName: "ubuntu",
		ContainerName: "ubuntu", Image: "docker.io/library/ubuntu:24.04", Tag: "24.04", Running: 1, IsMain: true, CodeDigest: "sha256:h",
	}}}); err != nil {
		t.Fatal(err)
	}
	telnet := "pkg:deb/ubuntu/telnet@0.17+2.5-3ubuntu4?arch=amd64&os_version=24.04&upstream=inetutils"
	_ = e.st.SetImageSBOM(ctx, sc, store.ImageSBOM{Repo: store.LambdaCodeRepo, Digest: "sha256:h", Source: "code", Purls: []string{telnet}})
	_ = e.st.SetPackageVulns(ctx, map[string][]string{telnet: {"UBUNTU-CVE-2026-24061"}})
	_ = e.st.SetVuln(ctx, store.Vuln{
		ID: "UBUNTU-CVE-2026-24061", Aliases: []string{"CVE-2026-24061"}, Severity: "CRITICAL",
		Fixes: map[string][]string{"Ubuntu:24.04:LTS|inetutils": {"2:2.5-3ubuntu4.1"}},
	})
	_ = e.st.SetKnownExploited(ctx, []store.Exploited{{CVE: "CVE-2026-24061", DateAdded: time.Now().Format(time.DateOnly)}})
	_, page := get(t, e.as(store.RoleViewer), e.srv.URL+"/updates", nil)
	for _, want := range []string{
		"exploited CVE-2026-24061", "server ", "/targets/" + host.ID, ">mail-01<",
		"<code>telnet 0.17+2.5-3ubuntu4</code>", "upgrade to 2:2.5-3ubuntu4.1",
	} {
		if !strings.Contains(page, want) {
			t.Errorf("updates page misses %q", want)
		}
	}
}

// A server's OS service page lists the server and its vulnerabilities, though the OS has no image digest.
func TestServiceSecurityOfServerOS(t *testing.T) {
	e := newUIEnv(t)
	ctx := context.Background()
	sc := e.ws.Scope()
	host, _ := e.st.CreateTarget(ctx, store.Target{Scope: sc, EnvironmentID: e.prod.ID, Platform: "host", Name: "mail-01"})
	osSvc, _ := e.st.EnsureService(ctx, sc, "ubuntu")
	if err := e.st.ApplySnapshot(ctx, store.SnapshotChanges{Scope: sc, TargetID: host.ID, SnapshotID: store.NewID(), At: time.Now(), Upsert: []store.Instance{{
		TargetID: host.ID, EnvironmentID: e.prod.ID, ServiceID: osSvc.ID, WorkloadID: "os/ubuntu", WorkloadKind: "host_os", WorkloadName: "ubuntu",
		ContainerName: "ubuntu", Image: "docker.io/library/ubuntu:24.04", Tag: "24.04", Running: 1, IsMain: true, CodeDigest: "sha256:h",
	}}}); err != nil {
		t.Fatal(err)
	}
	sudo := "pkg:deb/ubuntu/sudo@1.9.15p5-3ubuntu5?arch=amd64&os_version=24.04"
	_ = e.st.SetImageSBOM(ctx, sc, store.ImageSBOM{Repo: store.LambdaCodeRepo, Digest: "sha256:h", Source: "code", Purls: []string{sudo}})
	_ = e.st.SetPackageVulns(ctx, map[string][]string{sudo: {"UBUNTU-CVE-2025-32463"}})
	_ = e.st.SetVuln(ctx, store.Vuln{
		ID: "UBUNTU-CVE-2025-32463", Aliases: []string{"CVE-2025-32463"}, Severity: "CRITICAL",
		Fixes: map[string][]string{"Ubuntu:24.04:LTS|sudo": {"1.9.15p5-3ubuntu5.24.04.1"}},
	})
	_ = e.st.SetKnownExploited(ctx, []store.Exploited{{CVE: "CVE-2025-32463", DateAdded: "2025-09-29"}})
	_, page := get(t, e.as(store.RoleViewer), e.srv.URL+"/services/ubuntu", nil)
	for _, want := range []string{"ubuntu:24.04 on mail-01", "CVE-2025-32463", "1 exploited vulnerability"} {
		if !strings.Contains(page, want) {
			t.Errorf("ubuntu's page misses %q", want)
		}
	}
}

// A server's OS and packages are no images: the supply-chain evidence does not count them as not checked.
func TestEvidenceLeavesServersOut(t *testing.T) {
	e := newUIEnv(t)
	ctx := context.Background()
	sc := e.ws.Scope()
	viewer := e.as(store.RoleViewer)
	unchecked := func() string {
		_, page := get(t, viewer, e.srv.URL+"/security", nil)
		i := strings.Index(page, `id="evidence"`)
		if i < 0 {
			return "none"
		}
		sec := page[i:]
		j := strings.Index(sec, "Not checked")
		return strings.Join(strings.Fields(regexp.MustCompile(`<[^>]+>`).ReplaceAllString(sec[max(0, j-80):j], " ")), " ")
	}
	before := unchecked()
	host, _ := e.st.CreateTarget(ctx, store.Target{Scope: sc, EnvironmentID: e.prod.ID, Platform: "host", Name: "mail-01"})
	osSvc, _ := e.st.EnsureService(ctx, sc, "ubuntu")
	nginx, _ := e.st.EnsureService(ctx, sc, "nginx")
	if err := e.st.ApplySnapshot(ctx, store.SnapshotChanges{Scope: sc, TargetID: host.ID, SnapshotID: store.NewID(), At: time.Now(), Upsert: []store.Instance{
		{
			TargetID: host.ID, EnvironmentID: e.prod.ID, ServiceID: osSvc.ID, WorkloadID: "os/ubuntu", WorkloadKind: "host_os", WorkloadName: "ubuntu",
			ContainerName: "ubuntu", Image: "docker.io/library/ubuntu:24.04", Tag: "24.04", Running: 1, IsMain: true, CodeDigest: "sha256:h",
		},
		{
			TargetID: host.ID, EnvironmentID: e.prod.ID, ServiceID: nginx.ID, WorkloadID: "pkg/nginx", WorkloadKind: "host_package", WorkloadName: "nginx",
			ContainerName: "nginx", Image: "docker.io/library/nginx:1.24.0", Tag: "1.24.0", Running: 1, IsMain: true,
		},
	}}); err != nil {
		t.Fatal(err)
	}
	if after := unchecked(); after != before {
		t.Errorf("a server's OS and packages counted as images not checked: before %q, after %q", before, after)
	}
}
