// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package ui

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/pipozzz/goliash/internal/versions"
)

// autoPRActor signs the pull requests the version policy opens.
const autoPRActor = "auto-pr"

// RunAutoPRs opens, every interval, the pull requests services' version policies ask for (auto_pr), until ctx
// ends.
func (s *Server) RunAutoPRs(ctx context.Context, interval time.Duration) {
	if s.pulls == nil {
		return
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		if err := s.RefreshPulls(ctx); err != nil && ctx.Err() == nil {
			s.log.Error("reading pull requests' state failed", "err", err)
		}
		if n, err := s.AutoPRs(ctx); err != nil && ctx.Err() == nil {
			s.log.Error("automatic pull requests failed", "err", err)
		} else if n > 0 {
			s.log.Info("automatic pull requests opened", "count", n)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// AutoPRs opens a pull request for every GitOps application whose service's policy names its environment in
// auto_pr, when the newest acceptable release is at most that jump away and none was opened for it yet. It
// returns how many it opened.
func (s *Server) AutoPRs(ctx context.Context) (int, error) {
	if s.pulls == nil {
		return 0, nil
	}
	wss, err := s.store.ListWorkspaces(ctx)
	if err != nil {
		return 0, err
	}
	opened := 0
	for _, ws := range wss {
		sc := ws.Scope()
		if s.activeFreeze(ctx, sc) != "" { // a change freeze: the policy waits, people still can
			continue
		}
		o, err := versions.LoadOverview(ctx, s.store, sc)
		if err != nil {
			return opened, err
		}
		apps, err := versions.GitOpsApps(ctx, s.store, sc, o)
		if err != nil {
			return opened, err
		}
		for _, a := range apps {
			if a.Blocked(time.Now()) { // Argo CD would not sync it now: the policy waits for the window
				continue
			}
			svc := o.Services[a.ServiceID]
			pol, _, err := versions.PolicyFor(svc, o.Refs[a.ServiceID].Repo)
			if err != nil || pol.AutoPR[a.Environment] == "" {
				continue
			}
			up, ok := o.Upstreams[a.ServiceID]
			if !ok || !up.HasLatest {
				continue
			}
			// A product that cannot skip versions goes to its first stop: Kubernetes one minor at a time.
			to := s.pullRequestTo(a, versions.FirstStop(o, a.ServiceID, a.Tag, up.Latest.Raw))
			if to == "" {
				continue
			}
			running, ok := versions.ParseVersion(strings.TrimPrefix(a.Tag, "v"))
			next, okNext := versions.ParseVersion(strings.TrimPrefix(to, "v"))
			if !okNext {
				next = up.Latest
			}
			if !ok || !pol.AutoPR[a.Environment].AtLeast(running.JumpTo(next)) {
				continue // a bigger jump than the policy opens by itself
			}
			if _, done := s.pullFor(ctx, sc, a, to); done { // opened once: closed without merging is a no
				continue
			}
			if _, theirs := s.othersPullNow(ctx, a, to); theirs { // Renovate, Dependabot or a person proposed it
				continue
			}
			key := sc.WorkspaceID + "|" + a.TargetID + "|" + a.WorkloadID + "|" + to
			s.autoMu.Lock()
			failedAt, failed := s.autoFailed[key]
			s.autoMu.Unlock()
			if failed && time.Since(failedAt) < 24*time.Hour { // e.g. no file pins the tag: try again tomorrow
				continue
			}
			u := upgrade{
				app: a, service: svc.Name, page: "/services/" + svc.Name, to: to, notes: o.ReleaseURL[a.ServiceID+"|"+to],
				why: "the newest release its version policy accepts, opened by the policy (auto_pr: " + string(pol.AutoPR[a.Environment]) + ")",
			}
			u.vulns, _ = versions.UpgradeVulns(ctx, s.store, sc, a.ServiceID, envID(o, a.Environment), a.Tag, to)
			u.licenses, _ = versions.UpgradeLicenses(ctx, s.store, sc, a.ServiceID, envID(o, a.Environment), a.Tag, to)
			// What may break waits for a person: release notes that say so, or exploited vulnerabilities it brings.
			if hold := autoPRHold(versions.BreakingUpTo(o, a.ServiceID, a.Tag, to), u.vulns); hold != "" {
				s.autoMu.Lock()
				first := s.autoHeld[key] == ""
				s.autoHeld[key] = hold
				s.autoMu.Unlock()
				if first {
					s.log.InfoContext(ctx, "automatic pull request held for a person", "service", svc.Name, "environment", a.Environment, "to", to, "reason", hold)
				}
				continue
			}
			u.reviewers, u.labels = pol.PRReviewers, pol.PRLabels
			if _, msg := s.bump(ctx, sc, autoPRActor, u); msg != "" {
				s.log.WarnContext(ctx, "automatic pull request not opened", "service", svc.Name, "environment", a.Environment, "to", to, "reason", msg)
				s.autoMu.Lock()
				s.autoFailed[key] = time.Now()
				s.autoMu.Unlock()
				continue
			}
			opened++
		}
	}
	return opened, nil
}

// parseAutoPR reads "dev=minor, staging=patch".
func parseAutoPR(s string) (map[string]versions.Jump, error) {
	out := map[string]versions.Jump{}
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		env, jump, ok := strings.Cut(part, "=")
		env, j := strings.TrimSpace(env), versions.Jump(strings.TrimSpace(jump))
		if !ok || env == "" || (j != versions.JumpPatch && j != versions.JumpMinor && j != versions.JumpMajor) {
			return nil, fmt.Errorf("%q is not environment=patch, minor or major", part)
		}
		out[env] = j
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

// formatAutoPR writes auto_pr back for the form, environments in order.
func formatAutoPR(m map[string]versions.Jump) string {
	envs := make([]string, 0, len(m))
	for env := range m {
		envs = append(envs, env)
	}
	sort.Strings(envs)
	parts := make([]string, len(envs))
	for i, env := range envs {
		parts[i] = env + "=" + string(m[env])
	}
	return strings.Join(parts, ", ")
}

// autoPRHold is why an automatic pull request waits for a person, "" when it need not: its release notes say
// something breaks, or it brings vulnerabilities exploited in the wild.
func autoPRHold(breaking []string, vulns versions.VulnDelta) string {
	if len(breaking) > 0 {
		return "its release notes say something breaks: " + breaking[0]
	}
	if n := versions.Exploited(vulns.New); n > 0 {
		return fmt.Sprintf("it brings %d vulnerabilities exploited in the wild", n)
	}
	return ""
}
