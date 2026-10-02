// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package versions

import (
	"strings"
	"testing"
)

func TestParseImage(t *testing.T) {
	d := "sha256:" + strings.Repeat("a", 64)
	cases := map[string]ImageRef{
		"nginx":                                  {"docker.io", "library/nginx", "latest", ""},
		"nginx:1.27.2":                           {"docker.io", "library/nginx", "1.27.2", ""},
		"bitnami/redis:7.4":                      {"docker.io", "bitnami/redis", "7.4", ""},
		"docker.io/library/postgres:15.6-alpine": {"docker.io", "library/postgres", "15.6-alpine", ""},
		"index.docker.io/grafana/alloy:v1.4.0":   {"docker.io", "grafana/alloy", "v1.4.0", ""},
		"ghcr.io/acme/payments-api:1.4.2":        {"ghcr.io", "acme/payments-api", "1.4.2", ""},
		"localhost:5000/app":                     {"localhost:5000", "app", "latest", ""},
		"localhost/app:dev":                      {"localhost", "app", "dev", ""},
		"registry.example.com:8443/team/api:2.0": {"registry.example.com:8443", "team/api", "2.0", ""},
		"123456789012.dkr.ecr.eu-west-1.amazonaws.com/payments:1.5.0@" + d: {"123456789012.dkr.ecr.eu-west-1.amazonaws.com", "payments", "1.5.0", d},
		"nginx@" + d: {"docker.io", "library/nginx", "", d},
	}
	for in, want := range cases {
		if got := ParseImage(in); got != want {
			t.Errorf("ParseImage(%q)\n got %+v\nwant %+v", in, got, want)
		}
	}
	if r := ParseImage("ghcr.io/acme/payments-api:1.4.2"); r.Repo() != "ghcr.io/acme/payments-api" || r.Name() != "payments-api" {
		t.Errorf("Repo/Name = %s %s", r.Repo(), r.Name())
	}
}
