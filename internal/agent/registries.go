// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"context"
	"errors"
	"regexp"
	"time"

	"github.com/pipozzz/goliash/internal/collectors"
	"github.com/pipozzz/goliash/internal/registry"
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
	for _, c := range checks {
		r := agentproto.RegistryResult{Repository: c.Repository, Tags: []agentproto.RegistryTag{}}
		var creds registry.Credentials
		if c.CredentialsRef != nil && *c.CredentialsRef != "" {
			secret, err := collectors.Credential(*c.CredentialsRef)
			switch {
			case err == nil:
				creds = registry.ParseCredentials(secret)
			case !errors.Is(err, collectors.ErrNoCredential):
				msg := err.Error()
				r.Error = &msg
				out = append(out, r)
				continue
			}
		}
		tags, err := a.registry.ListTags(ctx, c.Repository, creds)
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
		out = append(out, r)
	}
	return out
}
