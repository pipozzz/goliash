// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package versions

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/pipozzz/goliash/internal/store"
)

func TestPurlImage(t *testing.T) {
	for in, want := range map[string]string{
		"pkg:docker/library/postgres":                              "docker.io/library/postgres",
		"pkg:docker/postgres":                                      "docker.io/library/postgres",
		"pkg:docker/bitnami/redis@sha256:abc":                      "docker.io/bitnami/redis",
		"pkg:docker/keycloak/keycloak?repository_url=quay.io":      "quay.io/keycloak/keycloak",
		"pkg:oci/airflow?repository_url=cgr.dev/chainguard":        "cgr.dev/chainguard/airflow",
		"pkg:oci/grafana?repository_url=docker.io/grafana/grafana": "docker.io/grafana/grafana",
		"pkg:oci/nothing":                                          "",
		"pkg:generic/postgresql":                                   "",
	} {
		if got := purlImage(in); got != want {
			t.Errorf("purlImage(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCycleFor(t *testing.T) {
	cycles := []EOLCycle{{Name: "16"}, {Name: "15"}, {Name: "7.2"}, {Name: "7"}, {Name: "1.27"}}
	for tag, want := range map[string]string{"15.6": "15", "16.4-alpine": "16", "7.2.4": "7.2", "7.4.0": "7", "1.27.3-alpine": "1.27", "14.1": ""} {
		c, ok := CycleFor(tag, cycles)
		if got := c.Name; (want == "" && ok) || got != want {
			t.Errorf("CycleFor(%q) = %q %v, want %q", tag, got, ok, want)
		}
	}
}

func TestEOLDrift(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/identifiers/purl":
			_, _ = w.Write([]byte(`{"result":[{"identifier":"pkg:docker/library/nginx","product":{"name":"nginx"}}]}`))
		case "/products/nginx":
			_, _ = w.Write([]byte(`{"result":{"releases":[
				{"name":"1.27","isEol":true,"eolFrom":"2025-04-16"},
				{"name":"1.28","isEol":false,"eolFrom":"` + time.Now().Add(20*24*time.Hour).Format("2006-01-02") + `"},
				{"name":"1.29","isEol":false,"eolFrom":"2027-04-01"}]}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	l := newLab(t)
	ctx := context.Background()
	e := NewEOL()
	e.BaseURL = srv.URL
	l.checker.SetEOL(e)

	drifts := func() map[string]DriftDetail {
		t.Helper()
		if err := l.checker.EvaluateDrift(ctx, l.sc); err != nil {
			t.Fatal(err)
		}
		open, _ := l.st.OpenDrifts(ctx, l.sc)
		out := map[string]DriftDetail{}
		for _, d := range open {
			if d.Kind == "eol" {
				out[d.EnvironmentID] = detailOf(t, d)
			}
		}
		return out
	}
	l.run(map[string]string{"stg-1": "1.29.0", "prod-a": "1.27.2"})
	got := drifts()
	if d, ok := got[l.envs["prod"].ID]; !ok || d.Other != "1.27" || d.EOL != "2025-04-16" {
		t.Fatalf("prod on an ended cycle: %+v", got)
	}
	if _, ok := got[l.envs["staging"].ID]; ok {
		t.Fatal("1.29 is supported until 2027")
	}

	l.run(map[string]string{"stg-1": "1.29.0", "prod-a": "1.28.1"})
	if d, ok := drifts()[l.envs["prod"].ID]; !ok || d.Other != "1.28" {
		t.Fatalf("a cycle ending within 60 days is reported: %+v", d)
	}

	l.svc.VersionPolicy = []byte(`{"eol":"none"}`)
	_ = l.st.UpdateService(ctx, l.svc)
	if got := drifts(); len(got) != 0 {
		t.Fatalf(`"eol": "none" turns it off: %+v`, got)
	}
}

func detailOf(t *testing.T, d store.Drift) DriftDetail {
	t.Helper()
	var det DriftDetail
	if err := json.Unmarshal(d.Detail, &det); err != nil {
		t.Fatal(err)
	}
	return det
}
