// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package ui

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/pipozzz/goliash/internal/auth"
	"github.com/pipozzz/goliash/internal/mapping"
	"github.com/pipozzz/goliash/internal/store"
)

// The clean-up page offers what is silent or unused, and leaves what reports.
func TestCleanup(t *testing.T) {
	e := newUIEnv(t)
	ctx := context.Background()
	sc := e.ws.Scope()
	quiet, _ := e.st.CreateAgent(ctx, sc, "old-gw", "hash-old")
	_, _ = e.st.CreateTarget(ctx, store.Target{Scope: sc, EnvironmentID: e.prod.ID, AgentID: quiet.ID, Platform: "docker", Name: "old-docker"})
	empty, _ := e.st.CreateEnvironment(ctx, sc, "qa", 40)
	gone, _ := e.st.EnsureService(ctx, sc, "retired")
	_, _ = e.st.CreateMappingRule(ctx, store.MappingRule{Scope: sc, Priority: 100, MatchType: "image_repo", Pattern: mapping.ImagePattern("public.ecr.aws/lambda/nodejs"), ServiceID: gone.ID})
	_, _ = e.st.CreateMappingRule(ctx, store.MappingRule{Scope: sc, Priority: 100, MatchType: "image_repo", Pattern: mapping.ImagePattern("ghcr.io/acme/api"), ServiceID: gone.ID})

	p := auth.Principal{User: store.User{OrgID: e.ws.OrgID, Role: store.RoleOwner}, Scope: sc}
	v, err := e.ui.cleanupView(ctx, p, time.Now().Add(8*24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	names := func(items []CleanupItem) string {
		var out []string
		for _, it := range items {
			out = append(out, it.Name)
		}
		return strings.Join(out, ",")
	}
	if names(v.Agents) != "old-gw" || names(v.Envs) != "qa" || !strings.Contains(names(v.Services), "retired") ||
		len(v.Rules) != 1 || !strings.Contains(v.Rules[0].Name, "lambda") {
		t.Fatalf("offered agents %q envs %q services %q rules %+v", names(v.Agents), names(v.Envs), names(v.Services), v.Rules)
	}
	if strings.Contains(names(v.Targets), "old-docker") {
		t.Error("a target offered on its own though its agent goes")
	}
	// The workspace's own target reported: today nothing is offered but the empty environment and the rule.
	if now, _ := e.ui.cleanupView(ctx, p, time.Now()); len(now.Agents) != 0 || names(now.Envs) != "qa" {
		t.Fatalf("today: %+v", now)
	}

	// Deleting an agent takes its targets; an environment goes.
	if err := e.ui.cleanupOne(ctx, p, "agents", v.Agents[0]); err != nil {
		t.Fatal(err)
	}
	if ts, _ := e.st.ListAgentTargets(ctx, sc, quiet.ID); len(ts) != 0 {
		t.Error("targets of the deleted agent stayed")
	}
	if err := e.ui.cleanupOne(ctx, p, "envs", CleanupItem{ID: empty.ID, Name: "qa"}); err != nil {
		t.Fatal(err)
	}
	admin := e.as(store.RoleAdmin)
	if _, page := get(t, admin, e.srv.URL+"/cleanup", nil); !strings.Contains(page, "Mapping rules that match nothing") {
		t.Fatal("clean-up page without the inert rule")
	}
}
