// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: Apache-2.0

package registry

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
)

func TestImageSBOMSources(t *testing.T) {
	const (
		built  = "sha256:" + "a100000000000000000000000000000000000000000000000000000000000000"
		signed = "sha256:" + "b100000000000000000000000000000000000000000000000000000000000000"
		orased = "sha256:" + "c100000000000000000000000000000000000000000000000000000000000000"
		bundle = "sha256:" + "d100000000000000000000000000000000000000000000000000000000000000"
		none   = "sha256:" + "e100000000000000000000000000000000000000000000000000000000000000"
		index  = "sha256:" + "f100000000000000000000000000000000000000000000000000000000000000"
		amd64  = "sha256:" + "f200000000000000000000000000000000000000000000000000000000000000"
	)
	spdx := `{"spdxVersion":"SPDX-2.3","packages":[{"name":"openssl","externalRefs":[{"referenceType":"purl","referenceLocator":"pkg:deb/debian/openssl@3.0.15"}]}]}`
	cdx := `{"bomFormat":"CycloneDX","components":[{"purl":"pkg:npm/lodash@4.17.15","components":[{"purl":"pkg:npm/minimist@1.2.5"}]}]}`
	statement := func(doc string) string {
		return `{"_type":"https://in-toto.io/Statement/v1","predicateType":"https://cyclonedx.org/bom","predicate":` + doc + `}`
	}
	dsse := `{"payloadType":"application/vnd.in-toto+json","payload":"` + base64.StdEncoding.EncodeToString([]byte(statement(cdx))) + `","signatures":[]}`
	sigstore := `{"mediaType":"application/vnd.dev.sigstore.bundle.v0.3+json","dsseEnvelope":` + dsse + `}`
	single := `{"mediaType":"` + mediaManifest + `","config":{"digest":"sha256:c"}}`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := map[string]string{
			// BuildKit: an in-toto statement with an SPDX predicate in the attestation manifest.
			"/v2/acme/app/manifests/" + built: `{"mediaType":"` + mediaIndex + `","manifests":[
				{"digest":"sha256:att","annotations":{"vnd.docker.reference.type":"attestation-manifest"}}]}`,
			"/v2/acme/app/manifests/sha256:att":  `{"layers":[{"digest":"sha256:spdxstmt","annotations":{"in-toto.io/predicate-type":"https://spdx.dev/Document"}}]}`,
			"/v2/acme/app/blobs/sha256:spdxstmt": `{"predicateType":"https://spdx.dev/Document","predicate":` + spdx + `}`,
			// cosign attest --type cyclonedx: a DSSE envelope in the .att tag.
			"/v2/acme/app/manifests/" + signed:                     single,
			"/v2/acme/app/manifests/sha256-" + signed[7:] + ".att": `{"layers":[{"digest":"sha256:prov","annotations":{"predicateType":"https://slsa.dev/provenance/v1"}},{"digest":"sha256:dsse","annotations":{"predicateType":"https://cyclonedx.org/bom"}}]}`,
			"/v2/acme/app/blobs/sha256:dsse":                       dsse,
			// oras attach --artifact-type application/vnd.cyclonedx+json: the bare document.
			"/v2/acme/app/manifests/" + orased:   single,
			"/v2/acme/app/referrers/" + orased:   `{"manifests":[{"digest":"sha256:oras","artifactType":"application/vnd.cyclonedx+json"}]}`,
			"/v2/acme/app/manifests/sha256:oras": `{"layers":[{"digest":"sha256:bare"}]}`,
			"/v2/acme/app/blobs/sha256:bare":     cdx,
			// A Sigstore bundle with an SBOM predicate as a referrer.
			"/v2/acme/app/manifests/" + bundle:     single,
			"/v2/acme/app/referrers/" + bundle:     `{"manifests":[{"digest":"sha256:bnd","artifactType":"application/vnd.dev.sigstore.bundle.v0.3+json","annotations":{"dev.sigstore.bundle.predicateType":"https://cyclonedx.org/bom"}}]}`,
			"/v2/acme/app/manifests/sha256:bnd":    `{"layers":[{"digest":"sha256:bundleblob"}]}`,
			"/v2/acme/app/blobs/sha256:bundleblob": sigstore,
			"/v2/acme/app/manifests/" + none:       single,
			// Chainguard: the index's SBOM lists its platform images; each platform has its own.
			"/v2/acme/app/manifests/" + index: `{"mediaType":"` + mediaIndex + `","manifests":[
				{"digest":"sha256:arm","platform":{"os":"linux","architecture":"arm64"}},
				{"digest":"` + amd64 + `","platform":{"os":"linux","architecture":"amd64"}}]}`,
			"/v2/acme/app/manifests/sha256-" + index[7:] + ".att": `{"layers":[{"digest":"sha256:idxsbom","annotations":{"predicateType":"https://spdx.dev/Document"}}]}`,
			"/v2/acme/app/blobs/sha256:idxsbom":                   `{"predicate":{"packages":[{"externalRefs":[{"referenceType":"purl","referenceLocator":"pkg:oci/image@sha256:arm"}]}]}}`,
			"/v2/acme/app/manifests/" + amd64:                     single,
			"/v2/acme/app/manifests/sha256-" + amd64[7:] + ".att": `{"layers":[{"digest":"sha256:dsse","annotations":{"predicateType":"https://cyclonedx.org/bom"}}]}`,
		}[r.URL.Path]
		if body == "" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()
	repo := strings.TrimPrefix(srv.URL, "http://") + "/acme/app"
	ctx := context.Background()
	for _, c := range []struct {
		name, digest string
		want         []string
	}{
		{"buildkit", built, []string{"pkg:deb/debian/openssl@3.0.15"}},
		{"cosign", signed, []string{"pkg:npm/lodash@4.17.15", "pkg:npm/minimist@1.2.5"}},
		{"oras", orased, []string{"pkg:npm/lodash@4.17.15", "pkg:npm/minimist@1.2.5"}},
		{"sigstore bundle", bundle, []string{"pkg:npm/lodash@4.17.15", "pkg:npm/minimist@1.2.5"}},
		{"none", none, []string{}},
		{"index of platforms", index, []string{"pkg:npm/lodash@4.17.15", "pkg:npm/minimist@1.2.5"}},
	} {
		got, err := client().ImageSBOM(ctx, repo, c.digest, Credentials{})
		if err != nil || !slices.Equal(got, c.want) {
			t.Errorf("%s: %v %v", c.name, got, err)
		}
	}
}
