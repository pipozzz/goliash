// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/pipozzz/goliash/internal/collectors"
	"github.com/pipozzz/goliash/internal/registry"
	"github.com/pipozzz/goliash/internal/registry/ecr"
	"github.com/pipozzz/goliash/pkg/agentproto"
)

// registryLoop checks the private repositories the server asks for, on the
// configured interval and right after the list changes.
func (a *Agent) registryLoop(ctx context.Context) {
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-a.registriesChanged:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(time.Second)
			continue
		case <-timer.C:
		}

		a.mu.Lock()
		checks := append([]agentproto.RegistryCheck(nil), a.cfg.Registries...)
		interval := time.Hour
		if s := a.cfg.RegistryCheckIntervalSeconds; s != nil && *s >= 300 {
			interval = time.Duration(*s) * time.Second
		}
		a.mu.Unlock()

		if len(checks) > 0 {
			res := agentproto.RegistryResults{CheckedAt: time.Now().UTC(), Results: a.checkRegistries(ctx, checks)}
			if err := a.client.registryResults(ctx, res); err != nil && ctx.Err() == nil {
				a.log.Warn("sending registry results failed", "err", err)
				interval = time.Minute
			}
		}
		timer.Reset(interval)
	}
}

func (a *Agent) checkRegistries(ctx context.Context, checks []agentproto.RegistryCheck) []agentproto.RegistryResult {
	out := make([]agentproto.RegistryResult, 0, len(checks))
	keys := a.keychain(ctx)
	for _, c := range checks {
		r := agentproto.RegistryResult{Repository: c.Repository, Tags: []agentproto.RegistryTag{}}
		tags, creds, err := a.listTags(ctx, c, keys)
		if err != nil {
			msg := err.Error()
			r.Error = &msg
			a.log.Warn("registry check failed", "repository", c.Repository, "err", err)
			out = append(out, r)
			continue
		}
		var filter *regexp.Regexp
		if c.TagFilter != nil {
			filter, _ = regexp.Compile(*c.TagFilter)
		}
		for _, t := range tags {
			if filter == nil || filter.MatchString(t) {
				r.Tags = append(r.Tags, agentproto.RegistryTag{Name: t})
			}
		}
		r.Resolved = a.resolveDigests(ctx, c, creds)
		out = append(out, r)
	}
	return out
}

// keychain gathers the registry credentials the agent finds by itself: Docker's
// config.json (DOCKER_CONFIG or ~/.docker), then the image pull secrets of the
// Kubernetes targets it collects, when it may read them.
func (a *Agent) keychain(ctx context.Context) registry.Keychain {
	k, err := registry.DockerConfigKeychain()
	if err != nil {
		a.log.Warn("docker config not readable", "err", err)
		k = registry.Keychain{}
	}
	a.mu.Lock()
	var sources []collectors.KeychainSource
	for _, rt := range a.runners {
		if s, ok := rt.runner.collector.(collectors.KeychainSource); ok {
			sources = append(sources, s)
		}
	}
	a.mu.Unlock()
	for _, s := range sources {
		found, err := s.RegistryKeychain(ctx)
		if err != nil {
			a.log.Warn("registry credentials from the cluster not readable", "err", err)
			continue
		}
		k.Merge(found)
	}
	return k
}

// listTags lists a repository's tags. The credential that credentials_ref (the
// registry host) resolves to locally comes first: for Amazon ECR the AWS profile to
// use (none: the default chain, e.g. the pod's or task's IAM role), for other
// registries "user:password" or a token. Without it, the credentials found in keys
// for the registry host are tried in turn, then anonymous access.
// It also returns the credentials that worked, for further reads of the repository.
func (a *Agent) listTags(ctx context.Context, c agentproto.RegistryCheck, keys registry.Keychain) ([]string, registry.Credentials, error) {
	none := registry.Credentials{}
	secret := ""
	if c.CredentialsRef != nil && *c.CredentialsRef != "" {
		var err error
		if secret, err = collectors.Credential(*c.CredentialsRef); err != nil && !errors.Is(err, collectors.ErrNoCredential) {
			return nil, none, err
		}
	}
	if _, _, _, ok := ecr.Parse(c.Repository); ok {
		tags, err := a.opts.ECR.ListTags(ctx, c.Repository, strings.TrimSpace(secret))
		return tags, none, err
	}
	if secret != "" {
		creds := registry.ParseCredentials(secret)
		tags, err := a.registry.ListTags(ctx, c.Repository, creds)
		return tags, creds, err
	}
	host, _, _ := strings.Cut(c.Repository, "/")
	for _, creds := range keys.Lookup(host) {
		tags, err := a.registry.ListTags(ctx, c.Repository, creds)
		if !errors.Is(err, registry.ErrUnauthorized) {
			return tags, creds, err
		}
	}
	var cloudErr error
	if a.opts.Cloud != nil {
		creds, ok, err := a.opts.Cloud.Credentials(ctx, host)
		switch {
		case ok && err == nil:
			tags, err := a.registry.ListTags(ctx, c.Repository, creds)
			if !errors.Is(err, registry.ErrUnauthorized) {
				return tags, creds, err
			}
			cloudErr = fmt.Errorf("the cloud identity was refused: %w", err)
		case ok:
			cloudErr = err
		}
	}
	tags, err := a.registry.ListTags(ctx, c.Repository, none)
	if errors.Is(err, registry.ErrUnauthorized) {
		err = fmt.Errorf("%w; give this agent a credential for %s: %s, a docker login, or an image pull secret", err, host, collectors.CredentialEnv(host))
		if cloudErr != nil {
			err = fmt.Errorf("%w (%v)", err, cloudErr)
		}
		return nil, none, err
	}
	return tags, none, err
}

// DigestReader reads the digests behind a tag; registry.Client in production.
type DigestReader interface {
	TagDigest(ctx context.Context, repository, reference string, creds registry.Credentials) (string, error)
	ManifestDigests(ctx context.Context, repository, reference string, creds registry.Credentials) ([]string, error)
}

// platformLookups bounds the candidates whose platform manifests are read (GET) when
// no manifest digest matched with HEAD.
const platformLookups = 2

// resolveDigests answers the server's lookups: which candidate tag each running
// digest is. HEAD requests come first (Docker Hub does not count them as pulls); the
// platform manifests of the newest candidates are read only when none matched.
func (a *Agent) resolveDigests(ctx context.Context, c agentproto.RegistryCheck, creds registry.Credentials) []agentproto.DigestMatch {
	reader, ok := a.registry.(DigestReader)
	if !ok || len(c.Resolve) == 0 {
		return nil
	}
	if _, _, _, isECR := ecr.Parse(c.Repository); isECR {
		return nil
	}
	out := make([]agentproto.DigestMatch, 0, len(c.Resolve))
	for _, l := range c.Resolve {
		m := agentproto.DigestMatch{Digest: l.Digest}
		for _, cand := range l.Candidates {
			if d, err := reader.TagDigest(ctx, c.Repository, cand, creds); err == nil && d == l.Digest {
				m.Tag = &cand
				break
			}
		}
		for _, cand := range l.Candidates[:min(len(l.Candidates), platformLookups)] {
			if m.Tag != nil {
				break
			}
			if ds, err := reader.ManifestDigests(ctx, c.Repository, cand, creds); err == nil && slices.Contains(ds, l.Digest) {
				m.Tag = &cand
			}
		}
		if ctx.Err() != nil {
			return out
		}
		out = append(out, m)
	}
	return out
}
