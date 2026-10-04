// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package versions

import (
	"testing"

	"github.com/pipozzz/goliash/internal/store"
)

func TestDeclaredVersions(t *testing.T) {
	envs := []store.Environment{{ID: "stg", Name: "staging"}, {ID: "prod", Name: "prod"}}
	targets := []store.Target{
		{ID: "k8s", Name: "k8s-prod", Platform: "kubernetes", EnvironmentID: "prod"},
		{ID: "git-prod", Name: "git-prod", Platform: "compose", EnvironmentID: "prod"},
		{ID: "git-stg", Name: "git-stg", Platform: "compose", EnvironmentID: "stg"},
	}
	services := []store.Service{{ID: "web", Name: "web"}, {ID: "api", Name: "api"}}
	inst := func(target, env, svc, tag string, running int) store.Instance {
		return store.Instance{TargetID: target, EnvironmentID: env, ServiceID: svc, Tag: tag, Running: running, IsMain: true, WorkloadID: target + svc}
	}
	active := []store.Instance{
		inst("k8s", "prod", "web", "1.4.2", 3),
		inst("git-prod", "prod", "web", "1.5.0", 1),
		inst("git-stg", "stg", "web", "1.5.0", 1),
		inst("k8s", "prod", "api", "2.0.0", 2),
		inst("git-prod", "prod", "api", "2.0.0", 1),
	}
	m := BuildMatrix(services, envs, targets, active)
	cells := map[string][]Cell{}
	for _, r := range m.Rows {
		cells[r.Service.ID] = r.Cells
	}

	stg, prod := cells["web"][0], cells["web"][1]
	if !stg.FromDeclared || stg.Primary().Tag != "1.5.0" {
		t.Fatalf("staging has only Compose files, so it shows the declared version: %+v", stg)
	}
	if prod.FromDeclared || prod.Primary().Tag != "1.4.2" || len(prod.Versions) != 1 || prod.Declared[0].Tag != "1.5.0" {
		t.Fatalf("prod shows what runs, with the declared version beside it: %+v", prod)
	}

	got := map[string]DriftDetail{}
	for _, d := range Drifts(m, nil, nil) {
		got[d.Service+"/"+d.Env+"/"+d.Kind] = d.Detail
	}
	if d, ok := got["web/prod/declared"]; !ok || d.Running != "1.4.2" || d.Other != "1.5.0" {
		t.Fatalf("declared drift for web in prod missing: %v", got)
	}
	if _, ok := got["web/prod/inconsistent"]; ok {
		t.Fatal("a Compose file is not a target that disagrees")
	}
	if _, ok := got["api/prod/declared"]; ok {
		t.Fatal("api runs what Git declares")
	}
	if _, ok := got["web/stg/declared"]; ok {
		t.Fatal("declared drift without anything running")
	}
	if _, ok := got["web/prod/env"]; !ok {
		t.Fatal("prod 1.4.2 behind staging 1.5.0 (declared) is still env drift")
	}
}
