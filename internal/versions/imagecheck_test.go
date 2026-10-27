// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package versions

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/pipozzz/goliash/internal/registry"
	"github.com/pipozzz/goliash/internal/store"
)

// checkRegistry is a registry holding one signed image with an SBOM attestation.
type checkRegistry struct {
	signer string
	sbom   []string
}

func (r checkRegistry) ListTags(context.Context, string, registry.Credentials) ([]string, error) {
	return nil, nil
}

func (r checkRegistry) TagDigest(context.Context, string, string, registry.Credentials) (string, error) {
	return "sha256:aaaa", nil
}

func (r checkRegistry) ManifestDigests(context.Context, string, string, registry.Credentials) ([]string, error) {
	return nil, nil
}

func (r checkRegistry) ImageEvidence(context.Context, string, string, registry.Credentials) (registry.Evidence, error) {
	ev := registry.Evidence{SBOM: len(r.sbom) > 0}
	if r.signer != "" {
		ev.Signed, ev.Signers = true, []registry.Signer{{Identity: r.signer}}
	}
	return ev, nil
}

func (r checkRegistry) ImageSBOM(context.Context, string, string, registry.Credentials) (registry.SBOM, error) {
	return registry.SBOM{Purls: r.sbom}, nil
}

func TestCheckImage(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, "sqlite://:memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ws, _ := st.EnsureDefaultWorkspace(ctx)
	sc := ws.Scope()
	eol := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/products/nginx" {
			_, _ = w.Write([]byte(`{"result":{"releases":[{"name":"1.25","isEol":true,"eolFrom":"2024-05-29"},{"name":"1.27","isEol":false,"eolFrom":"2027-04-01"}]}}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer eol.Close()

	// Known vulnerabilities of two packages: one exploited in the wild, one critical.
	log4j, ssl := "pkg:maven/org.apache.logging.log4j/log4j-core@2.14.1", "pkg:deb/debian/openssl@3.0.11"
	_ = st.SetPackageVulns(ctx, map[string][]string{log4j: {"GHSA-jfh8-c2jp-5v3q"}, ssl: {"DEBIAN-CVE-2024-0001"}})
	_ = st.SetVuln(ctx, store.Vuln{ID: "GHSA-jfh8-c2jp-5v3q", Aliases: []string{"CVE-2021-44228"}, Severity: "CRITICAL", Fixes: map[string][]string{}})
	_ = st.SetVuln(ctx, store.Vuln{ID: "DEBIAN-CVE-2024-0001", Aliases: []string{"CVE-2024-0001"}, Severity: "CRITICAL", Fixes: map[string][]string{}})
	_ = st.SetKnownExploited(ctx, []store.Exploited{{CVE: "CVE-2021-44228", Name: "Log4Shell"}})

	svc, _ := st.EnsureService(ctx, sc, "web")
	svc.VersionPolicy = []byte(`{"signer":"https://github.com/acme/*","eol":"nginx"}`)
	_ = st.UpdateService(ctx, svc)

	check := func(reg checkRegistry, req ImageCheckRequest) ImageCheck {
		t.Helper()
		c := NewChecker(st, reg, slog.New(slog.NewTextHandler(io.Discard, nil)), time.Hour)
		e := NewEOL()
		e.BaseURL = eol.URL
		c.SetEOL(e)
		res, err := c.CheckImage(ctx, sc, req)
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	rules := func(res ImageCheck) (blocking, other []string) {
		for _, f := range res.Findings {
			if f.Blocking {
				blocking = append(blocking, f.Rule)
			} else {
				other = append(other, f.Rule)
			}
		}
		return blocking, other
	}

	// Signed by the wrong identity, with Log4Shell, on a cycle past its end of life: blocked three times over.
	res := check(checkRegistry{signer: "https://github.com/evil/repo", sbom: []string{log4j, ssl}}, ImageCheckRequest{Image: "docker.io/library/nginx:1.25.4", Service: "web"})
	blocking, other := rules(res)
	if res.Passed || !slices.Equal(blocking, []string{RuleExploited, RuleEOL, RuleSigner}) || !slices.Equal(other, []string{RuleCritical}) {
		t.Fatalf("blocked: %v / %v %+v", blocking, other, res)
	}
	if !slices.Equal(res.Exploited, []string{"CVE-2021-44228"}) || !slices.Equal(res.Critical, []string{"CVE-2024-0001"}) || res.SBOMSource != "attestation" || res.EOL != "2024-05-29" {
		t.Errorf("details %+v", res)
	}

	// The right signer, a supported cycle, and the build's own SBOM without Log4Shell: passes, warning of the critical one.
	res = check(checkRegistry{signer: "https://github.com/acme/web/.github/workflows/release.yml@refs/heads/main"},
		ImageCheckRequest{Image: "docker.io/library/nginx:1.27.2", Service: "web", SBOM: []byte(`{"bomFormat":"CycloneDX","components":[{"purl":"` + ssl + `"}]}`)})
	if blocking, other := rules(res); !res.Passed || len(blocking) != 0 || !slices.Equal(other, []string{RuleCritical}) || res.SBOMSource != "request" {
		t.Errorf("passing: %v %v %+v", blocking, other, res)
	}

	// Unsigned where a signer is expected; critical made blocking; no SBOM at all.
	res = check(checkRegistry{}, ImageCheckRequest{Image: "docker.io/library/nginx:1.27.2", Service: "web", FailOn: []string{RuleUnsigned, RuleNoSBOM}})
	if blocking, _ := rules(res); res.Passed || !slices.Equal(blocking, []string{RuleUnsigned, RuleNoSBOM}) {
		t.Errorf("unsigned: %v %+v", blocking, res)
	}
	// none fails on nothing.
	if res := check(checkRegistry{}, ImageCheckRequest{Image: "docker.io/library/nginx:1.25.4", Service: "web", FailOn: []string{"none"}}); !res.Passed {
		t.Errorf("none: %+v", res)
	}
}
