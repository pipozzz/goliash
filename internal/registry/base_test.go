// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: Apache-2.0

package registry

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestCurrentBase(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := map[string]string{
			"/v2/library/node/manifests/22-alpine": `{"mediaType":"` + mediaIndex + `","manifests":[
				{"digest":"sha256:arm","platform":{"os":"linux","architecture":"arm64"}},
				{"digest":"sha256:amd","platform":{"os":"linux","architecture":"amd64"}}]}`,
			"/v2/library/node/manifests/sha256:amd": `{"mediaType":"` + mediaManifest + `","config":{"digest":"sha256:cfg"},
				"layers":[{"digest":"sha256:alpine"},{"digest":"sha256:node"}]}`,
			"/v2/library/node/blobs/sha256:cfg": `{"created":"2026-10-02T08:00:00Z","architecture":"amd64"}`,
		}[r.URL.Path]
		if body == "" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()
	host := strings.TrimPrefix(srv.URL, "http://")
	b, err := client().CurrentBase(context.Background(), host+"/library/node", "22-alpine", Credentials{})
	if err != nil || len(b.Layers) != 2 || !b.Created.Equal(time.Date(2026, 10, 2, 8, 0, 0, 0, time.UTC)) || !strings.HasPrefix(b.Digest, "sha256:") {
		t.Fatalf("base %+v %v", b, err)
	}
	if !b.BuiltOn([]string{"sha256:alpine", "sha256:node", "sha256:app"}) {
		t.Error("an image on the current base")
	}
	// Built on an older node:22-alpine: the same alpine layer, an older node one.
	if b.BuiltOn([]string{"sha256:alpine", "sha256:node-old", "sha256:app"}) || b.BuiltOn([]string{"sha256:alpine"}) {
		t.Error("an image on an older base counts as current")
	}
}
