// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package versions

import (
	"context"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/pipozzz/goliash/internal/store"
)

// GitOpsApp is a GitOps application (an Argo CD Application) as it deploys one workload of a service.
type GitOpsApp struct {
	store.WorkloadGitOps
	ServiceID   string
	Service     string
	Environment string
	Target      string
	Workload    string
	Image       string // the workload's main image, as it runs
	Tag         string // and its tag
}

// causeWindow is how long before an application's trouble began a version change on its cluster counts as its
// likeliest cause: a change days earlier, the application healthy since, is not.
const causeWindow = 24 * time.Hour

// Cause is the version change that likeliest caused the application's trouble: the newest change of its version on
// its cluster in the day before the trouble began (SyncSince). ok is false when there was none.
func Cause(ctx context.Context, st *store.Store, sc store.Scope, a GitOpsApp) (from string, at time.Time, ok bool) {
	return Replaced(ctx, st, sc, a, a.SyncSince.Add(-causeWindow))
}

// Replaced is the version the application's running version replaced on its cluster since since, and when:
// the newest such change. ok is false when none did.
func Replaced(ctx context.Context, st *store.Store, sc store.Scope, a GitOpsApp, since time.Time) (from string, at time.Time, ok bool) {
	evs, err := st.ListEvents(ctx, sc, store.EventFilter{ServiceID: a.ServiceID, Types: []string{"version_changed"}, Since: since})
	if err != nil {
		return "", time.Time{}, false
	}
	running := strings.TrimPrefix(a.Tag, "v")
	for _, e := range evs { // newest first
		if e.TargetID != a.TargetID || strings.TrimPrefix(e.ToVersion, "v") != running {
			continue
		}
		if e.FromVersion == "" || strings.TrimPrefix(e.FromVersion, "v") == running {
			return "", time.Time{}, false
		}
		return e.FromVersion, e.At, true
	}
	return "", time.Time{}, false
}

// gitOpsGrace is how long an application may be out of sync or unhealthy before it counts: Argo CD is out of
// sync for a moment on every change, and progressing while it rolls out.
const gitOpsGrace = 10 * time.Minute

// Trouble says what needs attention about the application, after the grace period: "out of sync", "degraded",
// "missing", or "" when it is fine (or only just changed).
func (a GitOpsApp) Trouble(now time.Time) string {
	if now.Sub(a.SyncSince) < gitOpsGrace {
		return ""
	}
	switch {
	case a.Health == "Degraded":
		return "degraded"
	case a.Health == "Missing":
		return "missing"
	case a.Sync == "OutOfSync":
		return "out of sync"
	}
	return ""
}

// GitOpsApps lists the GitOps applications of the workspace's mapped workloads, by service and environment.
func GitOpsApps(ctx context.Context, st *store.Store, sc store.Scope, o Overview) ([]GitOpsApp, error) {
	rows, err := st.ListWorkloadGitOps(ctx, sc)
	if err != nil || len(rows) == 0 {
		return nil, err
	}
	type key struct{ target, workload string }
	byWorkload := make(map[key]store.WorkloadGitOps, len(rows))
	for _, g := range rows {
		byWorkload[key{g.TargetID, g.WorkloadID}] = g
	}
	active, err := st.ListActiveInstances(ctx, sc)
	if err != nil {
		return nil, err
	}
	targetName := map[string]string{}
	for _, t := range o.Targets {
		targetName[t.ID] = t.Name
	}
	seen := map[key]bool{}
	var out []GitOpsApp
	for _, in := range active {
		k := key{in.TargetID, in.WorkloadID}
		g, ok := byWorkload[k]
		if !ok || seen[k] || in.ServiceID == "" || !in.IsMain { // the main container carries the version
			continue
		}
		seen[k] = true
		workload := in.WorkloadName
		if in.Namespace != "" {
			workload = in.Namespace + "/" + in.WorkloadName
		}
		out = append(out, GitOpsApp{
			WorkloadGitOps: g, ServiceID: in.ServiceID, Service: o.Services[in.ServiceID].Name,
			Environment: o.Envs[in.EnvironmentID].Name, Target: targetName[in.TargetID], Workload: workload,
			Image: in.Image, Tag: in.Tag,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Service != b.Service {
			return a.Service < b.Service
		}
		if a.Environment != b.Environment {
			return a.Environment < b.Environment
		}
		return a.App < b.App
	})
	return out, nil
}

// RepoWebURL turns a Git remote into the address of its web page, for GitHub, GitLab, Bitbucket, Gitea and the
// like: git@github.com:acme/deploy.git and https://github.com/acme/deploy.git both give
// https://github.com/acme/deploy. A Helm repository or anything else gives "".
func RepoWebURL(repo string) string {
	repo = strings.TrimSpace(repo)
	if rest, ok := strings.CutPrefix(repo, "git@"); ok { // scp-like: git@host:owner/repo.git
		host, path, found := strings.Cut(rest, ":")
		if !found {
			return ""
		}
		repo = "https://" + host + "/" + path
	}
	u, err := url.Parse(repo)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http" && u.Scheme != "ssh") || u.Host == "" {
		return ""
	}
	host := u.Hostname() // ssh://git@github.com:22/acme/deploy: no user, no SSH port
	if u.Scheme != "ssh" {
		host = u.Host
	}
	path := strings.TrimSuffix(strings.Trim(u.Path, "/"), ".git")
	if strings.Count(path, "/") < 1 { // owner/repo at least; a chart repository has no such path
		return ""
	}
	return "https://" + host + "/" + path
}

var commitSHA = regexp.MustCompile(`^[0-9a-f]{7,40}$`)

// CommitWebURL is the web page of a commit in a repository from RepoWebURL, or "" for a revision that is not a
// commit (a chart version).
func CommitWebURL(web, revision string) string {
	if web == "" || !commitSHA.MatchString(revision) {
		return ""
	}
	if strings.Contains(web, "gitlab") {
		return web + "/-/commit/" + revision
	}
	if strings.Contains(web, "bitbucket.org") {
		return web + "/commits/" + revision
	}
	return web + "/commit/" + revision
}

// CompareWebURL is the page of a repository's forge that shows what changed from one commit to another, or ""
// when either is not a commit or the forge is unknown (Bitbucket's needs more than the two).
func CompareWebURL(web, from, to string) string {
	if web == "" || !commitSHA.MatchString(from) || !commitSHA.MatchString(to) || strings.Contains(web, "bitbucket.org") {
		return ""
	}
	if strings.Contains(web, "gitlab") {
		return web + "/-/compare/" + from + "..." + to
	}
	return web + "/compare/" + from + "..." + to
}

// ArgoAppURL is the application's page in the Argo CD UI at base (GOLIASH_ARGOCD_URL), or "" without one.
func ArgoAppURL(base, appNamespace, app string) string {
	base = strings.TrimSuffix(strings.TrimSpace(base), "/")
	if base == "" || app == "" {
		return ""
	}
	if appNamespace == "" {
		return base + "/applications/" + url.PathEscape(app)
	}
	return base + "/applications/" + url.PathEscape(appNamespace) + "/" + url.PathEscape(app)
}

// GitOpsToolName is how a GitOps tool is called: "Argo CD", "Flux".
func GitOpsToolName(tool string) string {
	if tool == "flux" {
		return "Flux"
	}
	return "Argo CD"
}

// GitOpsObject is what the tool calls the object that deploys a workload: an Argo CD application, a Flux
// Kustomization or HelmRelease (told apart by a chart path: the HelmRelease's).
func GitOpsObject(tool string) string {
	if tool == "flux" {
		return "Flux"
	}
	return "Argo CD application"
}
