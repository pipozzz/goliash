// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package versions

import (
	"testing"

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
