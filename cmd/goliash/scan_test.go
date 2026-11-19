// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pipozzz/goliash/internal/ingest"
	"github.com/pipozzz/goliash/internal/store"
	"github.com/pipozzz/goliash/internal/versions"
)

// What scan reads: the named places, the local socket when nothing is named, and nothing that is switched off.
func TestScanTargets(t *testing.T) {
	none := func(string) string { return "" }
	sock := func(p string) bool { return p == "/var/run/docker.sock" }
	got := scanTargets("arn:aws:eks:eu-west-1:1:cluster/shop", "", false, false, none, sock)
	if len(got) != 2 || got[0].name != "shop" || got[0].platform != "kubernetes" || got[1].platform != "docker" {
		t.Fatalf("targets = %+v", got)
	}
	if d := got[1].settings["docker"].(map[string]any)["docker_host"]; d != "unix:///var/run/docker.sock" {
		t.Errorf("docker host = %v", d)
	}
	env := func(k string) string { return map[string]string{"DOCKER_HOST": "tcp://proxy:2375"}[k] }
	got = scanTargets("kind", "", true, false, env, sock)
	if len(got) != 1 || got[0].settings["docker"].(map[string]any)["docker_host"] != "tcp://proxy:2375" {
		t.Errorf("DOCKER_HOST not used, or Kubernetes read though switched off: %+v", got)
	}
	if got = scanTargets("kind", "", true, true, none, sock); len(got) != 0 {
		t.Errorf("everything off still scans %+v", got)
	}
}

// The workspace scan sets up collects its targets in the server and maps images by itself, and the settings it
// stores are the ones the collectors read.
func TestScanSetup(t *testing.T) {
	ctx := context.Background()
	db, ws, err := openDefault(ctx, filepath.Join(t.TempDir(), "goliash.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	targets := scanTargets("kind", "tcp://proxy:2375", false, false, func(string) string { return "" }, func(string) bool { return false })
	link, err := scanSetup(ctx, db, ws, targets, "prod", "http://localhost:8080", slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil || !strings.HasPrefix(link, "http://localhost:8080/") {
		t.Fatalf("link %q, err %v", link, err)
	}
	if on, _ := db.WorkspaceAutoMap(ctx, ws.ID); !on {
		t.Error("images are not mapped by themselves")
	}
	stored, err := db.ListServerTargets(ctx)
	if err != nil || len(stored) != 2 {
		t.Fatalf("server targets = %d, %v", len(stored), err)
	}
	for _, st := range stored {
		pt, err := ingest.ProtoTarget(st)
		if err != nil {
			t.Fatal(err)
		}
		switch st.Platform {
		case "kubernetes":
			if pt.Kubernetes == nil || pt.Kubernetes.KubeconfigContext == nil || *pt.Kubernetes.KubeconfigContext != "kind" {
				t.Errorf("kubernetes settings lost: %s", st.Settings)
			}
		case "docker":
			if pt.Docker == nil || pt.Docker.DockerHost != "tcp://proxy:2375" {
				t.Errorf("docker settings lost: %s", st.Settings)
			}
		}
	}
}

// The summary counts services, not drifts, and puts end of life and the biggest jump first.
func TestScanSummarize(t *testing.T) {
	detail := func(d versions.DriftDetail) json.RawMessage { b, _ := json.Marshal(d); return b }
	o := versions.Overview{
		Services:  map[string]store.Service{"a": {ID: "a", Name: "nginx"}, "b": {ID: "b", Name: "redis"}, "c": {ID: "c", Name: "app"}},
		CheckedAt: map[string]time.Time{"a": time.Now(), "b": time.Now()},
		CheckErrs: map[string]string{"c": "no comparable tag"},
		Drifts: map[string][]store.Drift{
			"a|e": {
				{ServiceID: "a", Kind: "upstream", Detail: detail(versions.DriftDetail{Running: "1.25.3", Other: "1.27.0", Jump: "minor"})},
				{ServiceID: "a", Kind: "eol", Detail: detail(versions.DriftDetail{Running: "1.25.3", Other: "1.25", EOL: "2024-05-29"})},
			},
			"b|e": {{ServiceID: "b", Kind: "upstream", Detail: detail(versions.DriftDetail{Running: "7.2.4", Other: "8.0.0", Jump: "major"})}},
		},
	}
	sum, done := summarize(o)
	if !done {
		t.Error("every service was looked at, one with an error: that is done")
	}
	if !strings.Contains(sum, "3 services run, 3 compared with their upstream, 2 behind.") {
		t.Errorf("summary: %s", sum)
	}
	eol, major, minor := strings.Index(sum, "end of life"), strings.Index(sum, "major"), strings.Index(sum, "minor")
	if eol < 0 || eol > major || major > minor {
		t.Errorf("order is not end of life, major, minor:\n%s", sum)
	}
	delete(o.CheckErrs, "c")
	if _, done := summarize(o); done {
		t.Error("a service not looked at yet: not done")
	}
}
