// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package ingest

import "testing"

func TestWorkloadApp(t *testing.T) {
	for _, c := range []struct {
		labels            map[string]string
		custom, ns        string
		wantApp, wantFrom string
		kind, name        string
	}{
		{map[string]string{"team-app": "billing", "app.kubernetes.io/part-of": "shop"}, "team-app", "prod", "billing", "team-app", "", ""},
		{map[string]string{"app.kubernetes.io/part-of": "shop", "app.kubernetes.io/instance": "shop-api"}, "", "prod", "shop", "app.kubernetes.io/part-of", "", ""},
		{map[string]string{"app.kubernetes.io/instance": "my-release"}, "team-app", "prod", "my-release", "app.kubernetes.io/instance", "", ""},
		{map[string]string{"goliash.app": "x", "app.kubernetes.io/part-of": "y"}, "", "", "x", "goliash.app", "", ""},
		{map[string]string{"com.docker.compose.project": "stack"}, "", "", "stack", "com.docker.compose.project", "", ""},
		{map[string]string{"app.kubernetes.io/name": "api"}, "", "payments", "payments", "namespace", "", ""},
		{nil, "", "", "", "", "", ""},
		{nil, "", "default", "gitea", "nomad job", "nomad_job", "gitea"},
		{map[string]string{"goliash.app": "forge"}, "", "default", "forge", "goliash.app", "nomad_job", "gitea"},
	} {
		app, from := workloadApp(c.labels, c.custom, c.ns, c.kind, c.name)
		if app != c.wantApp || from != c.wantFrom {
			t.Errorf("%v: %s from %s, want %s from %s", c.labels, app, from, c.wantApp, c.wantFrom)
		}
	}
}

func TestWithoutGeneratedSuffix(t *testing.T) {
	for in, want := range map[string]string{
		"cefiro-db-wruzyw":      "cefiro-db",
		"auth-authentik-ggmijo": "auth-authentik",
		"goliash-db-qm1ia8":     "goliash-db",
		"code-server":           "code-server",
		"gitea":                 "gitea",
		"my-app-Backup":         "my-app-Backup",
		"shop-api-v2":           "shop-api-v2",
	} {
		if got := withoutGeneratedSuffix(in); got != want {
			t.Errorf("%s -> %s, want %s", in, got, want)
		}
	}
	if app, src := workloadApp(nil, "", "default", "nomad_job", "cefiro-redis-gxs2nz"); app != "cefiro-redis" || src != "nomad job" {
		t.Errorf("nomad job %s %s", app, src)
	}
	if app, _ := workloadApp(map[string]string{"app.kubernetes.io/part-of": "shop-web-abc123"}, "", "", "", ""); app != "shop-web-abc123" {
		t.Error("an explicit label lost its suffix")
	}
}
