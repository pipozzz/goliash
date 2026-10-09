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

func TestImageEvidence(t *testing.T) {
	const (
		built  = "sha256:" + "aa00000000000000000000000000000000000000000000000000000000000000"
		bundle = "sha256:" + "bb00000000000000000000000000000000000000000000000000000000000000"
		plain  = "sha256:" + "cc00000000000000000000000000000000000000000000000000000000000000"
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := map[string]string{
			// BuildKit: an index with an attestation manifest carrying an SPDX SBOM and SLSA provenance.
			"/v2/acme/built/manifests/" + built: `{"mediaType":"` + mediaIndex + `","manifests":[
				{"digest":"sha256:amd","platform":{"os":"linux","architecture":"amd64"}},
				{"digest":"sha256:att","platform":{"os":"unknown","architecture":"unknown"},
				 "annotations":{"vnd.docker.reference.type":"attestation-manifest","vnd.docker.reference.digest":"sha256:amd"}}]}`,
			"/v2/acme/built/manifests/sha256:att": `{"mediaType":"` + mediaManifest + `","layers":[
				{"digest":"sha256:l1","annotations":{"in-toto.io/predicate-type":"https://spdx.dev/Document"}},
				{"digest":"sha256:l2","annotations":{"in-toto.io/predicate-type":"https://slsa.dev/provenance/v0.2"}}]}`,
			// cosign: a signature tag.
			"/v2/acme/built/manifests/sha256-" + built[7:] + ".sig": `{"mediaType":"` + mediaManifest + `","layers":[{"digest":"sha256:s"}]}`,
			// A Sigstore bundle as an OCI referrer.
			"/v2/acme/bundled/manifests/" + bundle: `{"mediaType":"` + mediaManifest + `","config":{"digest":"sha256:c"}}`,
			"/v2/acme/bundled/referrers/" + bundle: `{"mediaType":"` + mediaIndex + `","manifests":[
				{"digest":"sha256:r1","artifactType":"application/vnd.dev.sigstore.bundle.v0.3+json","annotations":{"dev.sigstore.bundle.predicateType":"https://slsa.dev/provenance/v1"}},
				{"digest":"sha256:r2","artifactType":"application/spdx+json"}]}`,
			"/v2/acme/plain/manifests/" + plain: `{"mediaType":"` + mediaManifest + `","config":{"digest":"sha256:c"}}`,
		}[r.URL.Path]
		if body == "" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()
	host := strings.TrimPrefix(srv.URL, "http://")
	ctx := context.Background()

	ev, err := client().ImageEvidence(ctx, host+"/acme/built", built, Credentials{})
	if err != nil || !ev.Signed || !ev.SBOM || !ev.Provenance {
		t.Errorf("buildkit and cosign: %+v %v", ev, err)
	}
	ev, err = client().ImageEvidence(ctx, host+"/acme/bundled", bundle, Credentials{})
	if err != nil || !ev.Signed || !ev.SBOM || !ev.Provenance {
		t.Errorf("referrers: %+v %v", ev, err)
	}
	ev, err = client().ImageEvidence(ctx, host+"/acme/plain", plain, Credentials{})
	if err != nil || ev.Signed || ev.SBOM || ev.Provenance || len(ev.Found) != 0 {
		t.Errorf("plain: %+v %v", ev, err)
	}
	if _, err := client().ImageEvidence(ctx, host+"/acme/missing", plain, Credentials{}); err == nil {
		t.Error("missing image: no error")
	}
}
