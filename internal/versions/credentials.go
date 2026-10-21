// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package versions

import (
	"context"
	"errors"
	"regexp"
	"slices"
	"strings"

	"github.com/pipozzz/goliash/internal/registry"
	"github.com/pipozzz/goliash/internal/store"
)

// RegistryAuth is what a workspace gave the server to read private registries, by normalized host.
type RegistryAuth map[string]registry.Credentials

// LoadRegistryAuth reads the workspace's registry credentials.
func LoadRegistryAuth(ctx context.Context, st *store.Store, sc store.Scope) (RegistryAuth, error) {
	all, err := st.ListRegistryCredentials(ctx, sc)
	if err != nil {
		return nil, err
	}
	out := RegistryAuth{}
	for _, c := range all {
		out[c.Host] = registry.Credentials{Username: c.Username, Password: c.Secret}
	}
	return out, nil
}

// For returns the credentials for repo's registry.
func (a RegistryAuth) For(repo string) (registry.Credentials, bool) {
	c, ok := a[RegistryHost(repo)]
	return c, ok
}

// Reads reports whether the server reads repo: on a public registry, or with the workspace's credentials.
func (a RegistryAuth) Reads(repo string) bool {
	_, ok := a.For(repo)
	return ok || IsPublicRegistry(repo)
}

// RegistryHost returns the normalized registry host of repo.
func RegistryHost(repo string) string {
	host, _, _ := strings.Cut(repo, "/")
	return registry.NormalizeHost(host)
}

// registryAuth loads the workspace's credentials; a failure leaves the server reading anonymously.
func (c *Checker) registryAuth(ctx context.Context, sc store.Scope) RegistryAuth {
	a, err := LoadRegistryAuth(ctx, c.store, sc)
	if err != nil {
		if ctx.Err() == nil {
			c.log.WarnContext(ctx, "registry credentials not readable", "err", err)
		}
		return RegistryAuth{}
	}
	return a
}

// noteAuth records whether the registry accepted the workspace's credentials for repo, so the settings
// show when a token expired. Errors other than a refusal say nothing about the credentials.
func (c *Checker) noteAuth(ctx context.Context, sc store.Scope, auth RegistryAuth, repo string, err error) {
	if _, ok := auth.For(repo); !ok || ctx.Err() != nil {
		return
	}
	failure := ""
	switch {
	case errors.Is(err, registry.ErrUnauthorized):
		failure = "The registry refused these credentials for " + repo + "."
	case err != nil:
		return
	}
	if err := c.store.RecordRegistryUse(ctx, sc, RegistryHost(repo), failure); err != nil {
		c.log.WarnContext(ctx, "record registry credential use", "err", err)
	}
}

// ErrNoRegistryClient means the checker has no registry client to try credentials with.
var ErrNoRegistryClient = errors.New("no registry client")

// TryRegistry lists the tags of one repository on host that the workspace's services use, with the
// workspace's credentials for host, and records whether the registry accepted them. repo is empty when
// no service uses host yet.
func (c *Checker) TryRegistry(ctx context.Context, sc store.Scope, host string) (repo string, tags int, err error) {
	st, err := c.load(ctx, sc)
	if err != nil {
		return "", 0, err
	}
	var repos []string
	for _, ref := range st.refs {
		if ref.Repo != "" && !PackageRepo(ref.Repo) && RegistryHost(ref.Repo) == host {
			repos = append(repos, ref.Repo)
		}
	}
	if len(repos) == 0 {
		return "", 0, nil
	}
	if c.tags == nil {
		return "", 0, ErrNoRegistryClient
	}
	repo = slices.Min(repos)
	c.mu.Lock()
	delete(c.cache, sc.WorkspaceID+"|"+repo)
	c.mu.Unlock()
	list, err := c.listTags(ctx, sc, repo, st.auth)
	return repo, len(list), err
}

// RefreshRegistry checks the workspace's upstreams, signatures and SBOMs again, after its registry
// credentials changed.
func (c *Checker) RefreshRegistry(ctx context.Context, sc store.Scope) {
	if err := c.CheckUpstreams(ctx, sc); err != nil && ctx.Err() == nil {
		c.log.WarnContext(ctx, "upstream check after new registry credentials", "err", err)
	}
	if err := c.CheckEvidence(ctx, sc); err != nil && ctx.Err() == nil {
		c.log.WarnContext(ctx, "evidence lookup after new registry credentials", "err", err)
	}
	if err := c.CheckVulnerabilities(ctx, sc); err != nil && ctx.Err() == nil {
		c.log.WarnContext(ctx, "vulnerability lookup after new registry credentials", "err", err)
	}
	_ = c.EvaluateDrift(ctx, sc)
}

// GoliashRepo is where Goliash's own server image is published.
const GoliashRepo = "ghcr.io/pipozzz/goliash"

// NewerGoliash returns the newest release of Goliash itself above running, from its public registry (the tag
// list is cached like any other), or "" when running is the newest or not a release.
func (c *Checker) NewerGoliash(ctx context.Context, running string) (string, error) {
	cur, ok := ParseVersion(strings.TrimPrefix(running, "v"))
	if !ok {
		return "", nil
	}
	tags, err := c.listTags(ctx, store.Scope{}, GoliashRepo, nil)
	if err != nil {
		return "", err
	}
	u := Latest(tags, cur.Raw, Policy{})
	if u.HasLatestAny && u.LatestAny.Compare(cur) > 0 {
		return u.LatestAny.Raw, nil
	}
	return "", nil
}

// goliashSecurityLine finds a change that fixes a vulnerability in release notes: a fix(security) commit, or one
// naming a Go vulnerability or a CVE.
var goliashSecurityLine = regexp.MustCompile(`(?i)fix\(security\)|\bGO-\d{4}-\d+\b|\bCVE-\d{4}-\d+\b`)

// goliashNoteLine strips a release note line down to its subject: "* <commit>: fix(security): … (#536) (@who)".
var goliashNoteLine = regexp.MustCompile(`^\s*[*-]\s*(?:[0-9a-f]{7,40}:\s*)?|\s*\(@[^)]*\)\s*$`)

// GoliashSecurityFixes lists the security fixes in Goliash's releases after from, up to and including to, as their
// release notes on GitHub name them; empty when none does, or the notes cannot be read.
func (c *Checker) GoliashSecurityFixes(ctx context.Context, from, to string) []string {
	svc := store.Service{VersionPolicy: []byte(`{"github":"pipozzz/goliash"}`)}
	notes, err := c.ReleaseNotes(ctx, svc, GoliashRepo, strings.TrimPrefix(from, "v"), strings.TrimPrefix(to, "v"))
	if err != nil {
		return nil
	}
	var out []string
	seen := map[string]bool{}
	for i := len(notes.Releases) - 1; i >= 0; i-- { // oldest first
		for _, line := range strings.Split(notes.Releases[i].Notes, "\n") {
			if !goliashSecurityLine.MatchString(line) {
				continue
			}
			s := strings.TrimSpace(goliashNoteLine.ReplaceAllString(line, ""))
			if s != "" && !seen[s] {
				seen[s] = true
				out = append(out, s)
			}
		}
	}
	return out
}
