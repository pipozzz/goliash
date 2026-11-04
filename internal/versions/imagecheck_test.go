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
	"github.com/pipozzz/goliash/internal/store/storetest"
	"github.com/pipozzz/goliash/pkg/agentproto"
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
	st, err := store.Open(ctx, storetest.DSN(t))
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

// An image of a registry only the agents read is asked of an agent, and checked once one read it: in CI, before
// it runs anywhere.
func TestCheckImageViaAgent(t *testing.T) {
	ctx := context.Background()
	l := newLab(t)
	l.run(map[string]string{"prod-a": "1.27.2"}) // runs payments from registry.internal.example, which the agents read
	const repo = "registry.internal.example/team/payments"
	log4j := "pkg:maven/org.apache.logging.log4j/log4j-core@2.14.1"
	_ = l.st.SetPackageVulns(ctx, map[string][]string{log4j: {"GHSA-jfh8-c2jp-5v3q"}})
	_ = l.st.SetVuln(ctx, store.Vuln{ID: "GHSA-jfh8-c2jp-5v3q", Aliases: []string{"CVE-2021-44228"}, Severity: "CRITICAL", Fixes: map[string][]string{}})
	_ = l.st.SetKnownExploited(ctx, []store.Exploited{{CVE: "CVE-2021-44228"}})

	check := func() ImageCheck {
		t.Helper()
		res, err := l.checker.CheckImage(ctx, l.sc, ImageCheckRequest{Image: repo + ":1.0.1"})
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	if res := check(); !res.Pending {
		t.Fatalf("not asked of an agent: %+v", res)
	}
	if toRead, _ := l.checker.releasesToRead(ctx, l.sc, nil); !slices.Contains(toRead[repo], "1.0.1") {
		t.Fatalf("the agents are not asked: %v", toRead)
	}
	digest := "sha256:3333333333333333333333333333333333333333333333333333333333333333"
	_ = l.st.SetReleaseImage(ctx, l.sc, store.ReleaseImage{Repo: repo, Tag: "1.0.1", Digest: digest})
	if res := check(); !res.Pending {
		t.Fatal("checked before its packages arrived")
	}
	_ = l.st.SetImageSBOM(ctx, l.sc, store.ImageSBOM{Repo: repo, Digest: digest, Purls: []string{log4j}, Source: "packages"})
	res := check()
	if res.Pending || res.Passed || res.Digest != digest || res.SBOMSource != "agent" || res.Packages != 1 || !slices.Contains(res.Exploited, "CVE-2021-44228") {
		t.Fatalf("check %+v", res)
	}
	for _, f := range res.Findings {
		if f.Rule == RuleUnreadable {
			t.Fatalf("unreadable after the agent read it: %+v", f)
		}
	}
}

// An image in a repository no service runs yet, on a registry the agents read: asked of them, by tag only.
func TestCheckNewRepositoryViaAgent(t *testing.T) {
	ctx := context.Background()
	l := newLab(t)
	l.run(map[string]string{"prod-a": "1.27.2"}) // runs payments from registry.internal.example, which the agents read
	const repo = "registry.internal.example/team/ledger"

	res, err := l.checker.CheckImage(ctx, l.sc, ImageCheckRequest{Image: repo + ":0.1.0"})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Pending {
		t.Fatalf("not asked of an agent: %+v", res)
	}
	checks, err := l.checker.PrivateRepositories(ctx, l.sc, "")
	if err != nil {
		t.Fatal(err)
	}
	i := slices.IndexFunc(checks, func(c agentproto.RegistryCheck) bool { return c.Repository == repo })
	if i < 0 {
		t.Fatalf("the agents are not asked: %+v", checks)
	}
	if c := checks[i]; !slices.Equal(c.ReleaseTags, []string{"0.1.0"}) || c.TagFilter == nil || *c.TagFilter != `^(0\.1\.0)$` {
		t.Fatalf("check %+v", c)
	}

	// A repository on a registry no agent reads: no one to ask.
	res, err = l.checker.CheckImage(ctx, l.sc, ImageCheckRequest{Image: "registry.elsewhere.example/team/ledger:0.1.0"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Pending {
		t.Fatalf("waits for no one: %+v", res)
	}
}
