// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package versions

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/pipozzz/goliash/internal/registry"
	"github.com/pipozzz/goliash/internal/store"
)

// The rules an image check applies, for goliash check in CI before a deploy.
const (
	RuleExploited  = "exploited"  // a package carries a vulnerability CISA lists as exploited in the wild
	RuleEOL        = "eol"        // the release cycle is past its end of life
	RuleUnsigned   = "unsigned"   // no signature, while the service's policy expects a signer
	RuleSigner     = "signer"     // signed, but by someone other than the policy's signer
	RuleCritical   = "critical"   // a package carries a critical vulnerability
	RuleNoSBOM     = "no-sbom"    // no SBOM, so its packages are unknown
	RuleUnreadable = "unreadable" // the registry could not be read
)

// CheckRules are every rule, in the order findings are listed.
var CheckRules = []string{RuleExploited, RuleEOL, RuleSigner, RuleUnsigned, RuleCritical, RuleNoSBOM, RuleUnreadable}

// DefaultBlocking are the rules that fail a check unless the request names others.
var DefaultBlocking = []string{RuleExploited, RuleEOL, RuleSigner, RuleUnsigned}

// ImageCheckRequest is what to check: an image reference, optionally the service it is (for its policy) and the
// SBOM the build made, and which rules fail the check (nil: DefaultBlocking; ["none"]: none).
type ImageCheckRequest struct {
	Image   string
	Service string
	SBOM    []byte
	FailOn  []string
}

// CheckFinding is one thing the check found.
type CheckFinding struct {
	Rule     string `json:"rule"`
	Blocking bool   `json:"blocking"`
	Detail   string `json:"detail"`
}

// ImageCheck is the verdict on an image.
type ImageCheck struct {
	Image      string         `json:"image"`
	Digest     string         `json:"digest,omitempty"`
	Service    string         `json:"service,omitempty"`
	Passed     bool           `json:"passed"`
	Findings   []CheckFinding `json:"findings"`
	Packages   int            `json:"packages"`
	SBOMSource string         `json:"sbom_source"` // request, attestation, packages (read from the image) or none
	Signed     bool           `json:"signed"`
	Signers    []string       `json:"signers,omitempty"`
	Exploited  []string       `json:"exploited,omitempty"`
	Critical   []string       `json:"critical,omitempty"`
	EOL        string         `json:"eol,omitempty"` // the end of life of its release cycle, when known
}

// osvPerCheck bounds the packages one check asks osv.dev about, and the vulnerabilities whose details it reads.
const (
	osvPerCheck     = 3000
	detailsPerCheck = 300
)

// CheckImage checks an image before it is deployed: the vulnerabilities of its packages (from the SBOM handed in,
// its SBOM attestation, or its package database), its signature against the service's policy, and the end of
// life of its release cycle.
func (c *Checker) CheckImage(ctx context.Context, sc store.Scope, req ImageCheckRequest) (ImageCheck, error) {
	ref := ParseImage(strings.TrimSpace(req.Image))
	repo := ref.Repo()
	out := ImageCheck{Image: req.Image, Digest: ref.Digest, Findings: []CheckFinding{}}
	blocking := req.FailOn
	if blocking == nil {
		blocking = DefaultBlocking
	}
	find := func(rule, detail string) {
		out.Findings = append(out.Findings, CheckFinding{Rule: rule, Blocking: slices.Contains(blocking, rule), Detail: detail})
	}

	// The service, for its policy: named, or the one running (or watching) this repository.
	svc, err := c.checkService(ctx, sc, req.Service, repo)
	if err != nil {
		return out, err
	}
	out.Service = svc.Name
	policy, _, _ := PolicyFor(svc, repo)

	auth := c.registryAuth(ctx, sc)
	creds, _ := auth.For(repo)
	readable := auth.Reads(repo)
	if out.Digest == "" && readable {
		if dr, ok := c.tags.(DigestReader); ok {
			if d, err := dr.TagDigest(ctx, repo, ref.Tag, creds); err == nil {
				out.Digest = d
			} else {
				find(RuleUnreadable, "the registry did not give the digest of "+ref.Tag+": "+err.Error())
			}
		}
	}
	if !readable {
		find(RuleUnreadable, "the server has no credentials for "+ref.Registry+"; hand in the SBOM, or add registry credentials")
	}

	// Signature.
	var ev registry.Evidence
	if er, ok := c.tags.(EvidenceReader); ok && out.Digest != "" && readable {
		if ev, err = er.ImageEvidence(ctx, repo, out.Digest, creds); err != nil {
			find(RuleUnreadable, "signatures and attestations: "+err.Error())
		}
	}
	out.Signed = ev.Signed
	for _, s := range ev.Signers {
		out.Signers = append(out.Signers, s.Identity)
	}
	if want := policy.Signer; want != "" && out.Digest != "" {
		switch {
		case !ev.Signed:
			find(RuleUnsigned, "not signed; the policy of "+svc.Name+" expects "+want)
		case !slices.ContainsFunc(ev.Signers, func(s registry.Signer) bool { return registry.SignerMatches(s.Identity, want) }):
			by := strings.Join(out.Signers, ", ")
			if by == "" {
				by = "a key, with no identity"
			}
			find(RuleSigner, "signed by "+by+", not "+want)
		}
	}

	// Packages: the SBOM handed in, else the image's SBOM attestation, else its package database.
	var sbom registry.SBOM
	switch {
	case len(req.SBOM) > 0:
		sbom, out.SBOMSource = registry.ParseSBOM(req.SBOM), "request"
	case ev.SBOM:
		if r, ok := c.tags.(SBOMReader); ok {
			if sbom, err = r.ImageSBOM(ctx, repo, out.Digest, creds); err == nil {
				out.SBOMSource = "attestation"
			}
		}
	case out.Digest != "" && readable:
		if r, ok := c.tags.(PackageReader); ok {
			if sbom, err = r.ImagePackages(ctx, repo, out.Digest, creds); err == nil && len(sbom.Purls) > 0 {
				out.SBOMSource = "packages"
			}
		}
	}
	if out.SBOMSource == "" {
		out.SBOMSource = "none"
		find(RuleNoSBOM, "no SBOM: its packages, and so their vulnerabilities, are unknown")
	}
	out.Packages = len(sbom.Purls)
	if err := c.checkVulns(ctx, sbom.Purls, &out); err != nil {
		return out, err
	}
	if len(out.Exploited) > 0 {
		find(RuleExploited, "exploited in the wild (CISA KEV): "+strings.Join(out.Exploited, ", "))
	}
	if len(out.Critical) > 0 {
		find(RuleCritical, fmt.Sprintf("%d critical vulnerabilities: %s", len(out.Critical), strings.Join(first(out.Critical, 5), ", ")))
	}

	// End of life of the release cycle.
	if c.eol != nil && policy.EOL != "none" {
		product := policy.EOL
		if product == "" {
			product, _ = c.eol.Product(ctx, repo)
		}
		if product != "" {
			if cycles, err := c.eol.Cycles(ctx, product); err == nil {
				if cy, ok := CycleFor(ref.Tag, cycles); ok {
					if !cy.EOLFrom.IsZero() {
						out.EOL = cy.EOLFrom.Format("2006-01-02")
					}
					if cy.IsEOL || (!cy.EOLFrom.IsZero() && !cy.EOLFrom.After(c.now())) {
						when := out.EOL
						if when == "" {
							when = "no known date"
						}
						find(RuleEOL, fmt.Sprintf("%s %s is past its end of life (%s)", product, cy.Name, when))
					}
				}
			}
		}
	}

	sort.SliceStable(out.Findings, func(i, j int) bool {
		return slices.Index(CheckRules, out.Findings[i].Rule) < slices.Index(CheckRules, out.Findings[j].Rule)
	})
	out.Passed = !slices.ContainsFunc(out.Findings, func(f CheckFinding) bool { return f.Blocking })
	return out, nil
}

// checkService is the named service, or the one whose running images (or upstream) are repo; zero when none.
func (c *Checker) checkService(ctx context.Context, sc store.Scope, name, repo string) (store.Service, error) {
	if name != "" {
		svc, err := c.store.GetServiceByName(ctx, sc, name)
		if err != nil {
			return svc, fmt.Errorf("no service %q", name)
		}
		return svc, nil
	}
	active, err := c.store.ListActiveInstances(ctx, sc)
	if err != nil {
		return store.Service{}, err
	}
	for _, in := range active {
		if in.ServiceID != "" && in.IsMain && ParseImage(in.Image).Repo() == repo {
			return c.store.GetService(ctx, sc, in.ServiceID)
		}
	}
	return store.Service{}, nil
}

// checkVulns finds the exploited and critical vulnerabilities of purls, asking osv.dev about those it has no fresh
// answer for.
func (c *Checker) checkVulns(ctx context.Context, purls []string, out *ImageCheck) error {
	if len(purls) == 0 {
		return nil
	}
	known, err := c.store.PackageVulnsOf(ctx, purls)
	if err != nil {
		return err
	}
	now := c.now()
	if c.osv != nil {
		var stale []string
		for _, p := range purls {
			if k, ok := known[p]; !ok || now.Sub(k.CheckedAt) >= osvTTL {
				stale = append(stale, p)
			}
		}
		if len(stale) > osvPerCheck {
			stale = stale[:osvPerCheck]
		}
		if len(stale) > 0 {
			answers, err := c.osv.QueryBatch(ctx, stale)
			if err != nil {
				return fmt.Errorf("osv.dev: %w", err)
			}
			if err := c.store.SetPackageVulns(ctx, answers); err != nil {
				return err
			}
			for p, ids := range answers {
				known[p] = store.PackageVulns{Purl: p, Vulns: ids, CheckedAt: now}
			}
		}
	}
	records, err := c.store.ListVulns(ctx)
	if err != nil {
		return err
	}
	kev, _, err := c.store.KnownExploited(ctx)
	if err != nil {
		return err
	}
	fetched := 0
	exploited, critical := map[string]bool{}, map[string]bool{}
	for _, p := range purls {
		for _, id := range known[p].Vulns {
			r, ok := records[id]
			if !ok && c.osv != nil && fetched < detailsPerCheck {
				fetched++
				if v, err := c.osv.Vuln(ctx, id); err == nil {
					r = store.Vuln{ID: v.ID, Aliases: v.Aliases, Summary: v.Summary, Severity: v.Severity, Fixes: v.Fixes}
					_ = c.store.SetVuln(ctx, r)
					records[id] = r
				}
			}
			key := CVEOf(id)
			for _, a := range r.Aliases {
				if cve := CVEOf(a); cve != "" {
					key = cve
					if _, hit := kev[cve]; hit {
						exploited[cve] = true
					}
				}
			}
			if _, hit := kev[key]; hit && key != "" {
				exploited[key] = true
			}
			if key == "" {
				key = id
			}
			if strings.EqualFold(r.Severity, "CRITICAL") && !exploited[key] {
				critical[key] = true
			}
		}
	}
	out.Exploited, out.Critical = keys(exploited), keys(critical)
	return nil
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func first(s []string, n int) []string {
	if len(s) > n {
		return append(s[:n:n], fmt.Sprintf("… %d more", len(s)-n))
	}
	return s
}
