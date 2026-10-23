// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package versions

import (
	"context"
	"strings"
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

// productUse is a product version an instance runs: a CPE product, its version, and how people name it.
type productUse struct {
	CPE, Version, Name string
	Own                bool // the image's own product, not a declared component
}

// githubSource marks a product looked up in its GitHub repository's advisories: "github:redis/redis".
const githubSource = "github:"

// fromGitHub reports whether a product use reads its repository's GitHub advisories.
func (u productUse) fromGitHub() bool { return strings.HasPrefix(u.CPE, githubSource) }

// productsOf is what an instance runs that NVD knows by product: the image's own product, when its service's
// policy (or the catalog) names its CPE, and the components its dev.goliash.components label declares.
func productsOf(svc store.Service, in store.Instance, resolved map[string]string, ev store.ImageEvidence) []productUse {
	var out []productUse
	if svc.ID != "" {
		if p, _, err := PolicyFor(svc, ParseImage(in.Image).Repo()); err == nil && (len(p.CPE) > 0 || p.GitHub != "") {
			if version := productVersion(in, resolved); version != "" {
				name := p.GitHub[strings.LastIndex(p.GitHub, "/")+1:]
				if len(p.CPE) > 0 {
					name = productName(p.CPE[0])
				}
				if p.GitHub != "" { // its own advisories first: they are what NVD is not asked about when there are any
					out = append(out, productUse{CPE: githubSource + p.GitHub, Version: version, Name: name, Own: true})
				}
				for _, c := range p.CPE {
					out = append(out, productUse{CPE: c, Version: version, Name: productName(c), Own: true})
				}
			}
		}
	}
	return append(out, parseComponents(ev.Components)...)
}

// componentCPEs are the NVD products of software a dev.goliash.components label names by its usual name.
var componentCPEs = map[string][]string{
	"nginx": {"f5:nginx", "f5:nginx_open_source"}, "php": {"php:php"}, "openssl": {"openssl:openssl"},
	"node": {"nodejs:node.js"}, "nodejs": {"nodejs:node.js"}, "python": {"python:python"}, "ruby": {"ruby-lang:ruby"},
	"go": {"golang:go"}, "golang": {"golang:go"}, "postgresql": {"postgresql:postgresql"}, "postgres": {"postgresql:postgresql"},
	"redis": {"redis:redis"}, "haproxy": {"haproxy:haproxy"}, "curl": {"haxx:curl"}, "libcurl": {"haxx:libcurl"},
	"httpd": {"apache:http_server"}, "apache": {"apache:http_server"}, "tomcat": {"apache:tomcat"},
	"openjdk": {"oracle:openjdk"}, "java": {"oracle:openjdk"}, "imagemagick": {"imagemagick:imagemagick"},
	"ffmpeg": {"ffmpeg:ffmpeg"}, "git": {"git-scm:git"}, "sqlite": {"sqlite:sqlite"}, "zlib": {"zlib:zlib"},
	"libxml2": {"xmlsoft:libxml2"}, "mariadb": {"mariadb:mariadb"}, "mysql": {"oracle:mysql_server", "oracle:mysql"},
	"memcached": {"memcached:memcached"}, "caddy": {"caddyserver:caddy"}, "traefik": {"traefik:traefik"},
	"envoy": {"envoyproxy:envoy"}, "keycloak": {"redhat:keycloak"}, "grafana": {"grafana:grafana"},
}

// parseComponents reads a dev.goliash.components label: name=version pairs separated by commas or spaces, a name
// being a usual one (nginx, php, openssl…) or an NVD product (vendor:product). Unknown names are left out.
func parseComponents(label string) []productUse {
	var out []productUse
	for _, field := range strings.FieldsFunc(label, func(r rune) bool { return r == ',' || r == ' ' || r == ';' }) {
		name, version, ok := strings.Cut(field, "=")
		name, version = strings.ToLower(strings.TrimSpace(name)), strings.TrimPrefix(strings.TrimSpace(version), "v")
		v, okV := ParseVersion(version)
		if !ok || name == "" || !okV || len(v.Parts) < minVersion {
			continue
		}
		cpes := componentCPEs[name]
		if strings.Count(name, ":") == 1 {
			cpes = []string{name}
		}
		for _, c := range cpes {
			out = append(out, productUse{CPE: c, Version: versionNumber(v), Name: productName(c)})
		}
	}
	return out
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
	if c.nvd == nil && c.github == nil {
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
	evidence, err := c.store.ListImageEvidence(ctx, sc)
	if err != nil {
		return err
	}
	resolved := resolvedVersions(ctx, c.store, sc)
	now, asked := c.now(), 0
	fresh := func(key string) bool { k, ok := known[key]; return ok && now.Sub(k.CheckedAt) < nvdTTL }
	for _, in := range active {
		uses := productsOf(services[in.ServiceID], in, resolved, evidence[ParseImage(in.Image).Repo()+"@"+in.Digest])
		// The product's own GitHub advisories, when its repository has any: then NVD is not asked about it.
		advised := false
		for _, use := range uses {
			if !use.fromGitHub() || c.github == nil {
				continue
			}
			key := use.CPE + "@" + use.Version
			if fresh(key) {
				advised = true
				continue
			}
			list, err := c.github.Advisories(ctx, strings.TrimPrefix(use.CPE, githubSource))
			if err != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				c.log.WarnContext(ctx, "GitHub advisories lookup failed", "repo", use.CPE, "err", err)
				continue
			}
			if len(list) == 0 {
				continue
			}
			advised = true
			vulns := AdvisoryVulns(list, use.Version, now)
			if err := c.store.SetProductVulns(ctx, use.CPE, use.Version, vulns); err != nil {
				return err
			}
			known[key] = store.ProductVulns{Product: use.CPE, Version: use.Version, Vulns: vulns, CheckedAt: now}
		}
		for _, use := range uses {
			if use.fromGitHub() || (use.Own && advised) || c.nvd == nil {
				continue
			}
			key := use.CPE + "@" + use.Version
			if fresh(key) || asked >= nvdPerRun {
				continue
			}
			asked++
			vulns, err := c.nvd.Query(ctx, use.CPE, use.Version)
			if err != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				c.log.WarnContext(ctx, "NVD lookup failed", "product", use.CPE, "version", use.Version, "err", err)
				continue
			}
			if err := c.store.SetProductVulns(ctx, use.CPE, use.Version, vulns); err != nil {
				return err
			}
			known[key] = store.ProductVulns{Product: use.CPE, Version: use.Version, Vulns: vulns, CheckedAt: now}
		}
	}
	return nil
}
