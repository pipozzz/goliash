// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: Apache-2.0

package registry

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func fakeImages(t *testing.T) string {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("registry got %s", r.Method)
		}
		if strings.Contains(r.URL.Path, "/manifests/") && !strings.Contains(r.Header.Get("Accept"), mediaIndex) {
			t.Errorf("manifest request without index media type: %q", r.Header.Get("Accept"))
		}
		body := map[string]string{
			// annotated index
			"/v2/acme/annotated/manifests/1.0": `{"mediaType":"` + mediaIndex + `","annotations":{"org.opencontainers.image.source":"https://github.com/acme/annotated"},"manifests":[]}`,
			// index -> linux/amd64 manifest -> config label
			"/v2/acme/labeled/manifests/1.0": `{"mediaType":"` + mediaIndex + `","manifests":[
				{"digest":"sha256:att","platform":{"os":"unknown","architecture":"unknown"}},
				{"digest":"sha256:arm","platform":{"os":"linux","architecture":"arm64"}},
				{"digest":"sha256:amd","platform":{"os":"linux","architecture":"amd64"}}]}`,
			"/v2/acme/labeled/manifests/sha256:amd": `{"mediaType":"` + mediaManifest + `","config":{"digest":"sha256:cfg"}}`,
			"/v2/acme/labeled/blobs/sha256:cfg":     `{"config":{"Labels":{"org.opencontainers.image.source":"https://github.com/acme/labeled.git"}}}`,
			// single manifest without a label
			"/v2/acme/plain/manifests/1.0":    `{"mediaType":"` + mediaDockerImage + `","config":{"digest":"sha256:cfg"}}`,
			"/v2/acme/plain/blobs/sha256:cfg": `{"config":{"Labels":{"maintainer":"x"}}}`,
		}[r.URL.Path]
		if body == "" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return strings.TrimPrefix(srv.URL, "http://")
}

func TestImageSource(t *testing.T) {
	host := fakeImages(t)
	for repo, want := range map[string]string{
		"acme/annotated": "https://github.com/acme/annotated",
		"acme/labeled":   "https://github.com/acme/labeled.git",
		"acme/plain":     "",
	} {
		got, err := client().ImageSource(context.Background(), host+"/"+repo, "1.0", Credentials{})
		if err != nil || got != want {
			t.Errorf("%s: %q, %v; want %q", repo, got, err, want)
		}
	}
	if _, err := client().ImageSource(context.Background(), host+"/acme/missing", "1.0", Credentials{}); err == nil {
		t.Error("missing image: no error")
	}
}

func TestGitHubRepository(t *testing.T) {
	for in, want := range map[string]string{
		"https://github.com/traefik/traefik":                                   "traefik/traefik",
		"https://github.com/acme/app.git":                                      "acme/app",
		"git@github.com:acme/app.git":                                          "acme/app",
		"git+https://github.com/acme/app":                                      "acme/app",
		"https://github.com/grafana/grafana/tree/main":                         "grafana/grafana",
		"https://www.github.com/a/b#readme":                                    "a/b",
		"https://github.com/nginxinc/docker-nginx.git#6a4c0cb:mainline/debian": "nginxinc/docker-nginx",
		"https://gitlab.com/acme/app":                                          "",
		"https://github.com/acme":                                              "",
		"https://github.com/acme/app\"><script>":                               "",
		"":                                                                     "",
	} {
		if got := GitHubRepository(in); got != want {
			t.Errorf("GitHubRepository(%q) = %q, want %q", in, got, want)
		}
	}
}
