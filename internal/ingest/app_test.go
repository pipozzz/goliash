// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package ingest

import "testing"

func TestWorkloadApp(t *testing.T) {
	for _, c := range []struct {
		labels            map[string]string
		custom, ns        string
		wantApp, wantFrom string
	}{
		{map[string]string{"team-app": "billing", "app.kubernetes.io/part-of": "shop"}, "team-app", "prod", "billing", "team-app"},
		{map[string]string{"app.kubernetes.io/part-of": "shop", "app.kubernetes.io/instance": "shop-api"}, "", "prod", "shop", "app.kubernetes.io/part-of"},
		{map[string]string{"app.kubernetes.io/instance": "my-release"}, "team-app", "prod", "my-release", "app.kubernetes.io/instance"},
		{map[string]string{"goliash.app": "x", "app.kubernetes.io/part-of": "y"}, "", "", "x", "goliash.app"},
		{map[string]string{"com.docker.compose.project": "stack"}, "", "", "stack", "com.docker.compose.project"},
		{map[string]string{"app.kubernetes.io/name": "api"}, "", "payments", "payments", "namespace"},
		{nil, "", "", "", ""},
	} {
		app, from := workloadApp(c.labels, c.custom, c.ns)
		if app != c.wantApp || from != c.wantFrom {
			t.Errorf("%v: %s from %s, want %s from %s", c.labels, app, from, c.wantApp, c.wantFrom)
		}
	}
}
