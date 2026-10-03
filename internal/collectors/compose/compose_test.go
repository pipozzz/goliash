// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: Apache-2.0

package compose

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pipozzz/goliash/pkg/agentproto"
)

const base = `
name: shop
services:
  web:
    image: nginx:${NGINX_TAG:-1.27.2}-alpine
    deploy: { replicas: 3 }
    labels: [goliash.service=shop-web, "com.example.team=web"]
  api:
    image: ghcr.io/acme/api:${API_TAG}
    labels: { goliash.env: staging }
  worker:
    build: ./worker
  cache:
    image: redis:7.2.5@sha256:` + "1111111111111111111111111111111111111111111111111111111111111111" + `
  price: { image: "acme/price:$$literal" }
  queue:
    image: rabbitmq:3.13
    deploy:
      replicas: ${QUEUE_REPLICAS:-4}
`

const override = `
services:
  web:
    scale: 2
  api:
    image: ghcr.io/acme/api:2.0.0
  worker:
    image: ghcr.io/acme/worker:0.9.0
`

func serve(t *testing.T, files map[string]string, wantToken string) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if wantToken != "" && r.Header.Get("Authorization") != "Bearer "+wantToken {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		body, ok := files[r.URL.Path]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func collect(t *testing.T, s agentproto.ComposeSettings, ref string) (map[string]agentproto.Workload, error) {
	t.Helper()
	target := agentproto.Target{Compose: &s}
	if ref != "" {
		target.CredentialsRef = &ref
	}
	c, err := NewRemote(context.Background(), target)
	if err != nil {
		return nil, err
	}
	res, err := c.Collect(context.Background())
	if err != nil {
		return nil, err
	}
	out := map[string]agentproto.Workload{}
	for _, w := range res.Workloads {
		out[w.Name] = w
	}
	return out, nil
}

func TestCollectMergesAndInterpolates(t *testing.T) {
	srv := serve(t, map[string]string{"/compose.yaml": base, "/compose.prod.yaml": override}, "")
	got, err := collect(t, agentproto.ComposeSettings{
		Files:     []string{srv.URL + "/compose.yaml", srv.URL + "/compose.prod.yaml"},
		Variables: map[string]string{"API_TAG": "1.9.0"},
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	if r := got["queue"].DesiredReplicas; r == nil || *r != 4 {
		t.Fatalf("replicas from a variable: %v", r)
	}
	if len(got) != 6 {
		t.Fatalf("services %v", got)
	}
	web := got["web"]
	if web.ID != "shop/web" || *web.Namespace != "shop" || web.Kind != agentproto.ComposeService {
		t.Fatalf("web %+v", web)
	}
	if c := web.Containers[0]; c.Image != "nginx:1.27.2-alpine" || c.Running != 2 || *web.DesiredReplicas != 2 {
		t.Fatalf("web: default tag and scale from the override: %+v", c)
	}
	if web.Labels["goliash.service"] != "shop-web" || web.Labels["com.example.team"] != "web" {
		t.Fatalf("labels %v", web.Labels)
	}
	if got["api"].Containers[0].Image != "ghcr.io/acme/api:2.0.0" || got["api"].Labels["goliash.env"] != "staging" {
		t.Fatalf("api: override wins, labels kept: %+v", got["api"])
	}
	if got["worker"].Containers[0].Image != "ghcr.io/acme/worker:0.9.0" {
		t.Fatal("a built service gets an image from an override")
	}
	if c := got["cache"].Containers[0]; c.Image != "redis:7.2.5" || c.Digest == nil {
		t.Fatalf("pinned digest: %+v", c)
	}
	if got["price"].Containers[0].Image != "acme/price:$literal" {
		t.Fatalf("$$ is a literal $: %s", got["price"].Containers[0].Image)
	}
}

func TestBuiltOnlyServicesAreSkipped(t *testing.T) {
	srv := serve(t, map[string]string{"/c.yml": base}, "")
	got, err := collect(t, agentproto.ComposeSettings{Files: []string{srv.URL + "/c.yml"}}, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := got["worker"]; ok {
		t.Fatal("worker has no image")
	}
	if got["api"].Containers[0].Image != "ghcr.io/acme/api:" {
		t.Fatalf("unset variable is empty: %s", got["api"].Containers[0].Image)
	}
}

func TestPrivateURLWithToken(t *testing.T) {
	srv := serve(t, map[string]string{"/c.yml": base}, "tok3n")
	t.Setenv("GOLIASH_CREDENTIAL_INFRA_REPO", "tok3n")
	if _, err := collect(t, agentproto.ComposeSettings{Files: []string{srv.URL + "/c.yml"}}, "infra-repo"); err != nil {
		t.Fatal(err)
	}
	_, err := collect(t, agentproto.ComposeSettings{Files: []string{srv.URL + "/c.yml?token=leak"}}, "")
	if err == nil || !strings.Contains(err.Error(), "HTTP 401") || strings.Contains(err.Error(), "leak") {
		t.Fatalf("err = %v", err)
	}
}

func TestErrorsDoNotQuoteTheFile(t *testing.T) {
	srv := serve(t, map[string]string{"/secret": "password: hunter2\nservices: [oops", "/notcompose": "password: hunter2\n"}, "")
	for _, path := range []string{"/secret", "/notcompose"} {
		_, err := collect(t, agentproto.ComposeSettings{Files: []string{srv.URL + path}}, "")
		if err == nil || strings.Contains(err.Error(), "hunter2") {
			t.Fatalf("%s: %v", path, err)
		}
	}
}

func TestLinkLocalAddressesAreRefused(t *testing.T) {
	_, err := collect(t, agentproto.ComposeSettings{Files: []string{"http://169.254.169.254/latest/meta-data/"}}, "")
	if err == nil || !strings.Contains(err.Error(), "not allowed") {
		t.Fatalf("metadata endpoint: %v", err)
	}
}

func TestLocalFiles(t *testing.T) {
	dir, outside := t.TempDir(), t.TempDir()
	stack := filepath.Join(dir, "compose.yaml")
	if err := os.WriteFile(stack, []byte(base), 0o600); err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(outside, "secret.yaml")
	_ = os.WriteFile(secret, []byte(base), 0o600)
	escape := filepath.Join(dir, "escape.yaml")
	if err := os.Symlink(secret, escape); err != nil {
		t.Fatal(err)
	}
	target := func(f string) agentproto.Target {
		return agentproto.Target{Compose: &agentproto.ComposeSettings{Files: []string{f}}}
	}

	if _, err := NewRemote(context.Background(), target(stack)); err == nil {
		t.Fatal("the server read a local file")
	}
	t.Setenv(DirsEnv, "")
	if _, err := New(context.Background(), target(stack)); err == nil || !strings.Contains(err.Error(), DirsEnv) {
		t.Fatalf("agent without allowed directories: %v", err)
	}
	t.Setenv(DirsEnv, dir)
	c, err := New(context.Background(), target(stack))
	if err != nil {
		t.Fatal(err)
	}
	if res, err := c.Collect(context.Background()); err != nil || len(res.Workloads) != 5 {
		t.Fatalf("local file: %v %v", res, err)
	}
	for _, f := range []string{secret, escape, filepath.Join(dir, "..", filepath.Base(outside), "secret.yaml")} {
		if _, err := New(context.Background(), target(f)); err == nil {
			t.Errorf("%s outside the allowed directory was accepted", f)
		}
	}
}

func TestInterpolate(t *testing.T) {
	vars := map[string]string{"SET": "x", "EMPTY": ""}
	for in, want := range map[string]string{
		"${SET}": "x", "${MISSING}": "", "$SET": "x", "$$SET": "$SET",
		"${EMPTY:-d}": "d", "${EMPTY-d}": "", "${MISSING-d}": "d",
		"${SET:+a}": "a", "${EMPTY:+a}": "", "${EMPTY+a}": "a", "${MISSING?err}": "",
	} {
		if got := interpolate(in, vars); got != want {
			t.Errorf("%s = %q, want %q", in, got, want)
		}
	}
}
