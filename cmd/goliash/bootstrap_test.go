// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/pipozzz/goliash/internal/store"
)

const bootYAML = `
owner: ops@example.com
environments:
  - { name: staging, position: 20 }
  - { name: prod, position: 30 }
targets:
  - name: nomad
    environment: prod
    platform: nomad
    poll_interval_seconds: 60
    settings:
      nomad: { address: "http://10.0.0.5:4646" }
      credentials_ref: nomad
`

func TestBootstrap(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, "sqlite://:memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	ws, _ := db.EnsureDefaultWorkspace(ctx)
	t.Setenv("GOLIASH_BOOTSTRAP", bootYAML)
	c, err := loadBootstrap()
	if err != nil {
		t.Fatal(err)
	}

	var logs bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logs, nil))
	for range 2 { // idempotent
		if err := applyBootstrap(ctx, db, ws, c, "https://goliash.example.com", log); err != nil {
			t.Fatal(err)
		}
	}
	envs, _ := db.ListEnvironments(ctx, ws.Scope())
	targets, _ := db.ListTargets(ctx, ws.Scope())
	if len(envs) != 2 || len(targets) != 1 || targets[0].PollIntervalSeconds != 60 || !strings.Contains(string(targets[0].Settings), `"credentials_ref":"nomad"`) {
		t.Fatalf("envs %+v targets %+v", envs, targets)
	}
	u, err := db.GetUserByEmail(ctx, ws.OrgID, "ops@example.com")
	if err != nil || u.Role != store.RoleOwner {
		t.Fatalf("owner %+v %v", u, err)
	}
	if n := strings.Count(logs.String(), "https://goliash.example.com/auth/magic?token="); n != 2 {
		t.Fatalf("want a sign-in link on every start before the first sign-in, got %d:\n%s", n, logs.String())
	}
}

func TestBootstrapRejectsTypos(t *testing.T) {
	t.Setenv("GOLIASH_BOOTSTRAP", "enviroments: [{name: prod}]")
	if _, err := loadBootstrap(); err == nil {
		t.Fatal("unknown field accepted")
	}
	t.Setenv("GOLIASH_BOOTSTRAP", "")
	if c, err := loadBootstrap(); c != nil || err != nil {
		t.Fatalf("no configuration: %v %v", c, err)
	}
}
