// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package ui

import (
	"strings"
	"testing"

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
