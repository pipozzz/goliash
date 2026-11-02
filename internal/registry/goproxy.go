// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: Apache-2.0

package registry

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/mod/module"
	"golang.org/x/mod/semver"
)

// A Go program built from a release's git checkout (Traefik, most vendors' images) records its own module's
// version as "(devel)" and only the commit it was built from. A Go module proxy knows the version tagged at that
// commit. GoProxy asks one, only for modules the Go vulnerability database lists: the names of a company's own
// modules, and their commits, are not sent anywhere, and a module with no advisories needs no version.

// Defaults of GoProxy.
const (
	DefaultGoProxyURL = "https://proxy.golang.org"
	DefaultGoVulnIdx  = "https://vuln.go.dev/index/modules.json"
	goIndexEvery      = 24 * time.Hour
	goIndexRetry      = time.Hour
	goVersionsMax     = 10000 // commits remembered
)

// GoProxy finds the version a Go module is tagged as at a commit.
type GoProxy struct {
	URL   string // the module proxy
	Index string // the Go vulnerability database's list of modules
	HTTP  *http.Client

	mu       sync.Mutex
	modules  map[string]bool
	fetched  time.Time // when modules was read
	tried    time.Time // the last attempt to read it
	versions map[string]string
}

// NewGoProxy asks proxy (DefaultGoProxyURL when empty).
func NewGoProxy(proxy string) *GoProxy {
	if proxy == "" {
		proxy = DefaultGoProxyURL
	}
	return &GoProxy{URL: strings.TrimSuffix(proxy, "/"), Index: DefaultGoVulnIdx, HTTP: &http.Client{Timeout: 30 * time.Second}}
}

// GoProxySetting is the GoProxy GOLIASH_GOPROXY names: nil for "off", the default proxy when empty.
func GoProxySetting(v string) *GoProxy {
	if v == "off" {
		return nil
	}
	return NewGoProxy(v)
}

// Version is the version of mod at the git commit rev: v3.1.0, or a pseudo-version for a commit no tag names;
// empty when mod has no advisories, or the proxy does not know the commit.
func (p *GoProxy) Version(ctx context.Context, mod, rev string) string {
	if !p.listed(ctx, mod) {
		return ""
	}
	key := mod + "@" + rev
	p.mu.Lock()
	v, ok := p.versions[key]
	p.mu.Unlock()
	if ok {
		return v
	}
	v, known := p.lookup(ctx, mod, rev)
	if known {
		p.mu.Lock()
		if p.versions == nil || len(p.versions) >= goVersionsMax {
			p.versions = map[string]string{}
		}
		p.versions[key] = v
		p.mu.Unlock()
	}
	return v
}

// lookup asks the proxy; known is false when it could not answer (unreachable), to ask again later.
func (p *GoProxy) lookup(ctx context.Context, mod, rev string) (v string, known bool) {
	path, err := module.EscapePath(mod)
	if err != nil {
		return "", true
	}
	var info struct{ Version string }
	status, err := p.getJSON(ctx, p.URL+"/"+path+"/@v/"+rev+".info", &info)
	switch {
	case status == http.StatusNotFound, status == http.StatusGone:
		return "", true
	case err != nil:
		return "", false
	case !semver.IsValid(info.Version):
		return "", true
	}
	return info.Version, true
}

// listed reports whether the Go vulnerability database has advisories for mod, reading its list of modules once
// a day.
func (p *GoProxy) listed(ctx context.Context, mod string) bool {
	p.mu.Lock()
	stale := time.Since(p.fetched) > goIndexEvery && time.Since(p.tried) > goIndexRetry
	if stale {
		p.tried = time.Now()
	}
	p.mu.Unlock()
	if stale {
		var idx []struct{ Path string }
		if _, err := p.getJSON(ctx, p.Index, &idx); err == nil {
			mods := make(map[string]bool, len(idx))
			for _, m := range idx {
				mods[m.Path] = true
			}
			p.mu.Lock()
			p.modules, p.fetched = mods, time.Now()
			p.mu.Unlock()
		}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.modules[mod]
}

func (p *GoProxy) getJSON(ctx context.Context, url string, v any) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, err
	}
	resp, err := p.HTTP.Do(req)
	if err != nil {
		return 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return resp.StatusCode, fmt.Errorf("%s: %s", url, resp.Status)
	}
	return resp.StatusCode, json.NewDecoder(resp.Body).Decode(v)
}
