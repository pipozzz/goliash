// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package versions

import (
	"context"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/pipozzz/goliash/catalog"
	"github.com/pipozzz/goliash/internal/store"
)

// Beside a fix upstream, Goliash says which version to run: the newest release of the product, and of the cycle
// that runs when it is still supported. endoflife.date knows the cycles of most products and the newest release of
// each; for the others, the project's GitHub releases give the newest one.

const releasesTTL = 24 * time.Hour

// productEOL and productGitHub name, for NVD products the catalog has no image for, their endoflife.date product
// and their GitHub repository.
var (
	productEOL = map[string]string{
		"openssl:openssl": "openssl", "sqlite:sqlite": "sqlite", "git-scm:git": "git", "ffmpeg:ffmpeg": "ffmpeg",
		"apache:http_server": "apache-http-server", "apache:tomcat": "tomcat", "oracle:openjdk": "openjdk-builds-from-oracle",
		"imagemagick:imagemagick": "imagemagick", "nodejs:node.js": "nodejs", "python:python": "python", "php:php": "php",
		"ruby-lang:ruby": "ruby", "golang:go": "go", "postgresql:postgresql": "postgresql", "redis:redis": "redis",
		"haproxy:haproxy": "haproxy", "mariadb:mariadb": "mariadb", "oracle:mysql_server": "mysql", "f5:nginx": "nginx",
		"f5:nginx_open_source": "nginx", "grafana:grafana": "grafana", "redhat:keycloak": "keycloak", "traefik:traefik": "traefik",
		"envoyproxy:envoy": "envoy", "caddyserver:caddy": "caddy",
	}
	productGitHub = map[string]string{
		"haxx:curl": "curl/curl", "haxx:libcurl": "curl/curl", "zlib:zlib": "madler/zlib", "openssl:openssl": "openssl/openssl",
		"imagemagick:imagemagick": "ImageMagick/ImageMagick", "memcached:memcached": "memcached/memcached",
	}
)

// releaseSources says where a product's releases are read: its endoflife.date product and GitHub repository.
func releaseSources(cpe string) (eol, github string) {
	eol, github = productEOL[cpe], productGitHub[cpe]
	if all, err := catalog.All(); err == nil {
		for _, e := range all {
			if !slices.Contains(e.CPE, cpe) {
				continue
			}
			if eol == "" {
				eol = e.EOL
			}
			if github == "" {
				github = e.GitHub
			}
		}
	}
	return eol, github
}

// tagVersionRe finds the version in a release tag: "release-1.29.3", "openssl-3.4.0", "curl-8_11_0", "v2.1.0".
var tagVersionRe = regexp.MustCompile(`\d+(?:[._]\d+){1,3}$`)

// tagVersion reads a release tag as a version, its prefix aside.
func tagVersion(tag string) (Version, bool) {
	m := tagVersionRe.FindString(tag)
	if m == "" {
		return Version{}, false
	}
	return ParseVersion(strings.ReplaceAll(m, "_", "."))
}

// productReleases asks endoflife.date, else GitHub, for a product's cycles and their newest releases.
func (c *Checker) productReleases(ctx context.Context, cpe string) ([]store.ProductCycle, error) {
	eol, github := releaseSources(cpe)
	var lastErr error
	if eol != "" && c.eol != nil {
		cycles, err := c.eol.Cycles(ctx, eol)
		if err == nil && len(cycles) > 0 {
			out := make([]store.ProductCycle, 0, len(cycles))
			for _, cy := range cycles {
				if cy.Latest != "" {
					out = append(out, store.ProductCycle{Name: cy.Name, Latest: cy.Latest, EOL: cy.IsEOL})
				}
			}
			if len(out) > 0 {
				return out, nil
			}
		}
		lastErr = err
	}
	if github != "" && c.github != nil {
		rels, err := c.github.Releases(ctx, github)
		if err != nil {
			return nil, err
		}
		var best Version
		latest := ""
		for _, r := range rels {
			v, ok := tagVersion(r.Tag)
			if r.Draft || !ok || v.Pre != "" {
				continue
			}
			if latest == "" || v.Compare(best) > 0 {
				best, latest = v, versionNumber(v)
			}
		}
		if latest != "" {
			return []store.ProductCycle{{Latest: latest}}, nil
		}
	}
	return nil, lastErr
}

// refreshProductReleases keeps the releases of the products upstream fixes are about, a day at a time.
func (c *Checker) refreshProductReleases(ctx context.Context, cpes []string) error {
	known, err := c.store.ListProductReleases(ctx)
	if err != nil {
		return err
	}
	now := c.now()
	for _, cpe := range cpes {
		if k, ok := known[cpe]; ok && now.Sub(k.CheckedAt) < releasesTTL {
			continue
		}
		cycles, err := c.productReleases(ctx, cpe)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			c.log.WarnContext(ctx, "product releases lookup failed", "product", cpe, "err", err)
		}
		if err := c.store.SetProductReleases(ctx, cpe, cycles); err != nil {
			return err
		}
		known[cpe] = store.ProductReleases{Product: cpe, Cycles: cycles, CheckedAt: now}
	}
	return nil
}

// newestReleases picks, from a product's cycles, its newest release, and the newest of the cycle that runs when it
// is still supported and not the newest overall.
func newestReleases(cycles []store.ProductCycle, running string) (latest, inCycle, cycle string) {
	var best Version
	eol := make([]EOLCycle, 0, len(cycles))
	for _, cy := range cycles {
		if v, ok := ParseVersion(cy.Latest); ok && (latest == "" || v.Compare(best) > 0) {
			best, latest = v, cy.Latest
		}
		if cy.Name != "" {
			eol = append(eol, EOLCycle{Name: cy.Name, IsEOL: cy.EOL, Latest: cy.Latest})
		}
	}
	if cy, ok := CycleFor(running, eol); ok && !cy.IsEOL && cy.Latest != "" && cy.Latest != latest {
		inCycle, cycle = cy.Latest, cy.Name
	}
	return latest, inCycle, cycle
}
