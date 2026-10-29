// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: Apache-2.0

package registry

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const openVEX = `{"@context":"https://openvex.dev/ns/v0.2.0","author":"Docker Hardened Images","statements":[
	{"vulnerability":{"name":"CVE-2023-44487"},"status":"not_affected","justification":"vulnerable_code_not_present",
	 "products":[{"@id":"pkg:docker/acme/app","subcomponents":[{"@id":"pkg:deb/debian/nginx@1.27.1-1~bookworm"}]}]},
	{"vulnerability":{"name":"CVE-2024-0001"},"status":"under_investigation"},
	{"vulnerability":{"name":"CVE-2024-0001"},"status":"fixed"}]}`

func TestParseVEX(t *testing.T) {
	statement := `{"_type":"https://in-toto.io/Statement/v1","predicateType":"https://openvex.dev/ns/v0.2.0","predicate":` + openVEX + `}`
	dsse := `{"payloadType":"application/vnd.in-toto+json","payload":"` + base64.StdEncoding.EncodeToString([]byte(statement)) + `"}`
	for name, body := range map[string]string{"bare": openVEX, "in-toto": statement, "dsse": dsse} {
		got := ParseVEX([]byte(body))
		if len(got) != 2 || got[0].Vulnerability != "CVE-2023-44487" || got[0].Status != "not_affected" ||
			got[0].Justification != "vulnerable_code_not_present" || got[0].Author != "Docker Hardened Images" ||
			len(got[0].Packages) != 1 || got[0].Packages[0] != "pkg:deb/debian/nginx@1.27.1-1~bookworm" ||
			got[1].Vulnerability != "CVE-2024-0001" || got[1].Status != "fixed" || len(got[1].Packages) != 0 {
			t.Errorf("%s: %+v", name, got)
		}
	}
	// OpenVEX 0.0.1 names the vulnerability as a string.
	if got := ParseVEX([]byte(`{"statements":[{"vulnerability":"CVE-2022-1","status":"not_affected","impact_statement":"not used"}]}`)); len(got) != 1 ||
		got[0].Vulnerability != "CVE-2022-1" || got[0].Impact != "not used" {
		t.Errorf("v0.0.1: %+v", got)
	}
	if got := ParseVEX([]byte(`{"spdxVersion":"SPDX-2.3"}`)); len(got) != 0 {
		t.Errorf("an SBOM: %+v", got)
	}
}

func TestImageVEX(t *testing.T) {
	const (
		built = "sha256:" + "a100000000000000000000000000000000000000000000000000000000000000"
		attd  = "sha256:" + "b100000000000000000000000000000000000000000000000000000000000000"
	)
	single := `{"mediaType":"` + mediaManifest + `","config":{"digest":"sha256:c"}}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := map[string]string{
			// docker buildx --attest type=vex: an attestation manifest layer.
			"/v2/acme/app/manifests/" + built: `{"mediaType":"` + mediaIndex + `","manifests":[
				{"digest":"sha256:att","annotations":{"vnd.docker.reference.type":"attestation-manifest"}}]}`,
			"/v2/acme/app/manifests/sha256:att": `{"layers":[{"digest":"sha256:sbom","annotations":{"in-toto.io/predicate-type":"https://spdx.dev/Document"}},
				{"digest":"sha256:vex","annotations":{"in-toto.io/predicate-type":"https://openvex.dev/ns/v0.2.0"}}]}`,
			"/v2/acme/app/blobs/sha256:vex": `{"predicateType":"https://openvex.dev/ns/v0.2.0","predicate":` + openVEX + `}`,
			// cosign attest --type openvex, and a later statement as a referrer.
			"/v2/acme/app/manifests/" + attd:                     single,
			"/v2/acme/app/manifests/sha256-" + attd[7:] + ".att": `{"layers":[{"digest":"sha256:vex","annotations":{"predicateType":"https://openvex.dev/ns"}}]}`,
			"/v2/acme/app/referrers/" + attd:                     `{"manifests":[{"digest":"sha256:ref","artifactType":"application/vnd.openvex+json"}]}`,
			"/v2/acme/app/manifests/sha256:ref":                  `{"layers":[{"digest":"sha256:later"}]}`,
			"/v2/acme/app/blobs/sha256:later":                    `{"statements":[{"vulnerability":{"name":"CVE-2024-0001"},"status":"affected"}]}`,
		}[r.URL.Path]
		if body == "" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()
	repo := strings.TrimPrefix(srv.URL, "http://") + "/acme/app"
	got, err := client().ImageVEX(context.Background(), repo, built, Credentials{})
	if err != nil || len(got) != 2 || got[1].Status != "fixed" {
		t.Errorf("buildkit: %+v %v", got, err)
	}
	got, err = client().ImageVEX(context.Background(), repo, attd, Credentials{})
	if err != nil || len(got) != 2 || got[1].Vulnerability != "CVE-2024-0001" || got[1].Status != "affected" {
		t.Errorf("cosign and referrer: %+v %v", got, err)
	}
	ev, _ := client().ImageEvidence(context.Background(), repo, built, Credentials{})
	if !ev.VEX || !strings.Contains(strings.Join(ev.Found, ","), "buildkit vex") {
		t.Errorf("evidence: %+v", ev)
	}
}
