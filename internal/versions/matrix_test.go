// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package versions

import (
	"testing"
	"time"

	"github.com/pipozzz/goliash/internal/store"
)

func TestBuildMatrix(t *testing.T) {
	envs := []store.Environment{{ID: "dev", Name: "dev"}, {ID: "prod", Name: "prod"}}
	services := []store.Service{{ID: "s-pay", Name: "payments"}, {ID: "s-idle", Name: "idle"}, {ID: "s-web", Name: "web"}}
	targets := []store.Target{{ID: "t1", Name: "eu-1"}, {ID: "t2", Name: "us-1"}}
	inst := func(svc, env, target, tag string, running int, main bool) store.Instance {
		return store.Instance{
			ServiceID: svc, EnvironmentID: env, TargetID: target, Tag: tag, Running: running, IsMain: main,
			WorkloadID: svc + target,
		}
	}
	m := BuildMatrix(services, envs, targets, []store.Instance{
		inst("s-pay", "dev", "t1", "1.6.0", 1, true),
		inst("s-pay", "prod", "t1", "1.5.0", 3, true),
		inst("s-pay", "prod", "t2", "1.5.0", 2, true),
		inst("s-pay", "prod", "t2", "1.4.2", 1, true),  // straggler on us-1
		inst("s-pay", "prod", "t1", "v1.31", 3, false), // sidecar ignored
		inst("s-web", "prod", "t1", "1.27.2", 2, true),
		inst("", "prod", "t1", "2.0.0", 1, true), // inbox
		inst("", "prod", "t2", "2.0.0", 1, false),
	})

	if len(m.Rows) != 2 || m.Rows[0].Service.Name != "payments" || m.Rows[1].Service.Name != "web" {
		t.Fatalf("rows %+v", m.Rows)
	}
	if m.Unmapped != 1 {
		t.Fatalf("unmapped = %d", m.Unmapped)
	}
	dev, prod := m.Rows[0].Cells[0], m.Rows[0].Cells[1]
	if dev.Primary().Tag != "1.6.0" || len(dev.Versions) != 1 {
		t.Fatalf("dev %+v", dev)
	}
	if len(prod.Versions) != 2 || prod.Primary().Tag != "1.5.0" || prod.Primary().Running != 5 ||
		len(prod.Primary().Targets) != 2 || prod.Versions[1].Tag != "1.4.2" || prod.Versions[1].Targets[0] != "us-1" {
		t.Fatalf("prod %+v", prod)
	}
	if !m.Rows[1].Cells[0].Empty() {
		t.Fatal("web should not run in dev")
	}
}

func TestStaleTarget(t *testing.T) {
	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	tg := store.Target{PollIntervalSeconds: 300, LastSnapshotAt: now.Add(-10 * time.Minute)}
	if StaleTarget(tg, false, now) {
		t.Fatal("10 minutes is within 15")
	}
	tg.LastSnapshotAt = now.Add(-16 * time.Minute)
	if !StaleTarget(tg, false, now) {
		t.Fatal("16 minutes without a snapshot is stale")
	}
	tg.LastSnapshotAt = now.Add(-time.Minute)
	if !StaleTarget(tg, true, now) {
		t.Fatal("a silent agent makes its targets stale")
	}
	if StaleTarget(store.Target{}, true, now) {
		t.Fatal("a target that never reported is not stale, just empty")
	}
}

func TestCommonApp(t *testing.T) {
	for _, c := range []struct {
		counts      map[[2]string]int
		app, source string
	}{
		{nil, "", ""},
		{map[[2]string]int{{"shop-prod", "namespace"}: 5, {"webshop", "app.kubernetes.io/part-of"}: 1}, "webshop", "app.kubernetes.io/part-of"},
		{map[[2]string]int{{"b", "release"}: 2, {"a", "release"}: 2, {"c", "release"}: 1}, "a", "release"},
		{map[[2]string]int{{"shop-dev", "namespace"}: 1, {"shop-prod", "namespace"}: 3}, "shop-prod", "namespace"},
	} {
		if app, src := commonApp(c.counts); app != c.app || src != c.source {
			t.Errorf("commonApp(%v) = %s/%s, want %s/%s", c.counts, app, src, c.app, c.source)
		}
	}
}

// One postgres image used by several applications: versions are compared within each
// application, so different databases on different versions are not drift, while an
// application behind its own staging still is.
func TestMatrixSplitsByApplication(t *testing.T) {
	envs := []store.Environment{{ID: "stg", Name: "staging"}, {ID: "prod", Name: "prod"}}
	services := []store.Service{{ID: "s-pg", Name: "postgres"}, {ID: "s-web", Name: "web"}}
	targets := []store.Target{{ID: "t1", Name: "dp"}, {ID: "t2", Name: "nomad"}}
	inst := func(svc, env, target, app, tag string) store.Instance {
		return store.Instance{
			ServiceID: svc, EnvironmentID: env, TargetID: target, Tag: tag, Running: 1, IsMain: true,
			WorkloadID: svc + target + app + env, App: app, AppSource: "com.docker.compose.project",
		}
	}
	m := BuildMatrix(services, envs, targets, []store.Instance{
		inst("s-pg", "prod", "t1", "auth", "18-alpine"),
		inst("s-pg", "prod", "t1", "chat", "16-alpine"),
		inst("s-pg", "prod", "t2", "gitea", "17"),
		inst("s-pg", "stg", "t1", "chat", "17-alpine"),
		inst("s-web", "stg", "t1", "shop-staging", "1.1.0"), // one app per environment: no split
		inst("s-web", "prod", "t1", "shop", "1.0.0"),
	})
	pg, web := m.Rows[0], m.Rows[1]
	if len(pg.Parts) != 3 || pg.Parts[0].App != "auth" || pg.Parts[1].App != "chat" || pg.Parts[2].App != "gitea" {
		t.Fatalf("parts %+v", pg.Parts)
	}
	if web.Parts != nil || len(web.Units()) != 1 {
		t.Fatalf("web split: %+v", web.Parts)
	}
	if got := pg.Parts[1].Cells[1].Primary().Tag; got != "16-alpine" {
		t.Fatalf("chat in prod runs %s", got)
	}

	var kinds []string
	for _, d := range Drifts(m, nil, nil) {
		kinds = append(kinds, d.Service+"/"+d.App+"/"+d.Env+"/"+d.Kind)
	}
	want := []string{"s-pg/chat/prod/env", "s-web//prod/env"}
	if len(kinds) != len(want) || kinds[0] != want[0] || kinds[1] != want[1] {
		t.Fatalf("drifts %v, want %v", kinds, want)
	}
}
