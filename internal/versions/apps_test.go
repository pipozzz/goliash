// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package versions

import (
	"strings"
	"testing"
)

func TestAppFamilies(t *testing.T) {
	fams := AppFamilies([]AppName{
		{"cefiro-cefiro", "nomad job"},
		{"cefiro-db", "nomad job"},
		{"cefiro-redis", "nomad job"},
		{"goliash", "nomad job"},
		{"goliash-db", "nomad job"},
		{"code-server", "nomad job"},
		{"auth-authentik", "namespace"},
		{"shop-api", "app.kubernetes.io/part-of"},
		{"shop-web", "app.kubernetes.io/part-of"},
		{"gitea", "nomad job"},
	})
	for app, want := range map[string]string{
		"cefiro-db": "cefiro", "cefiro-cefiro": "cefiro", "goliash-db": "goliash", "goliash": "goliash",
		"code-server": "code-server", "auth-authentik": "auth-authentik", "shop-api": "shop-api", "gitea": "gitea",
	} {
		if got := fams[app].Name; got != want {
			t.Errorf("%s in %q, want %q", app, got, want)
		}
	}
	if m := strings.Join(fams["cefiro-db"].Members, ","); m != "cefiro-cefiro,cefiro-db,cefiro-redis" {
		t.Errorf("members %s", m)
	}
	if len(fams["gitea"].Members) != 0 {
		t.Error("a lone app has members")
	}
}
