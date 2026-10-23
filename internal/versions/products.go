// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package versions

import (
	"context"
	"time"

	"github.com/pipozzz/goliash/internal/store"
)

// SetNVD sets where product vulnerabilities are looked up; nil turns it off.
func (c *Checker) SetNVD(n *NVD) { c.nvd = n }

const (
	nvdTTL     = 24 * time.Hour // how long NVD's answer for a product version is trusted
	nvdPerRun  = 8              // product versions asked per workspace and run (NVD is slow without a key)
	minVersion = 2              // parts a version needs to be looked up: 16.2, not 16
)

// productVersion is the product version an instance runs, for NVD: the number of its tag (16.2-alpine is 16.2),
// or of the version its digest was resolved to when the tag moves (postgres:16). "" when it is not precise enough.
func productVersion(in store.Instance, resolved map[string]string) string {
	for _, s := range []string{in.Tag, resolved[ParseImage(in.Image).Repo()+"@"+in.Digest]} {
		if v, ok := ParseVersion(s); ok && len(v.Parts) >= minVersion {
			return versionNumber(v)
		}
	}
	return ""
}

// productOf is the CPE products and version an instance runs, when its service's policy (or the catalog) names
// them.
func productOf(svc store.Service, in store.Instance, resolved map[string]string) ([]string, string) {
	if svc.ID == "" {
		return nil, ""
	}
	p, _, err := PolicyFor(svc, ParseImage(in.Image).Repo())
	if err != nil || len(p.CPE) == 0 {
		return nil, ""
	}
	version := productVersion(in, resolved)
	if version == "" {
		return nil, ""
	}
	return p.CPE, version
}

// resolvedVersions maps repo@digest to the version a moving tag was resolved to.
func resolvedVersions(ctx context.Context, st *store.Store, sc store.Scope) map[string]string {
	out := map[string]string{}
	rs, err := st.TagResolutions(ctx, sc)
	if err != nil {
		return out
	}
	for _, r := range rs {
		if r.Version != "" {
			out[r.Repo+"@"+r.Digest] = r.Version
		}
	}
	return out
}

// CheckProductVulns asks NVD which CVEs affect the product versions running, for services whose image is a known
// product (the catalog's CPE, or the version policy's).
func (c *Checker) CheckProductVulns(ctx context.Context, sc store.Scope) error {
	if c.nvd == nil {
		return nil
	}
	active, err := c.store.ListActiveInstances(ctx, sc)
	if err != nil {
		return err
	}
	services, err := c.servicesByID(ctx, sc)
	if err != nil {
		return err
	}
	known, err := c.store.ListProductVulns(ctx)
	if err != nil {
		return err
	}
	resolved := resolvedVersions(ctx, c.store, sc)
	now, asked := c.now(), 0
	for _, in := range active {
		products, version := productOf(services[in.ServiceID], in, resolved)
		for _, product := range products {
			key := product + "@" + version
			if k, ok := known[key]; (ok && now.Sub(k.CheckedAt) < nvdTTL) || asked >= nvdPerRun {
				continue
			}
			asked++
			vulns, err := c.nvd.Query(ctx, product, version)
			if err != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				c.log.WarnContext(ctx, "NVD lookup failed", "product", product, "version", version, "err", err)
				continue
			}
			if err := c.store.SetProductVulns(ctx, product, version, vulns); err != nil {
				return err
			}
			known[key] = store.ProductVulns{Product: product, Version: version, Vulns: vulns, CheckedAt: now}
		}
	}
	return nil
}
