// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package ui

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/pipozzz/goliash/internal/auth"
	"github.com/pipozzz/goliash/internal/gitops"
	"github.com/pipozzz/goliash/internal/versions"
)

// PullRequester opens an upgrade as a pull request in a GitOps repository.
type PullRequester interface {
	OpenBump(ctx context.Context, b gitops.Bump) (string, error)
}

// pullRequestTo is the version a pull request for the application would pin: the newest acceptable release, when
// it is newer than what runs and the application deploys from a GitHub repository; else "".
func (s *Server) pullRequestTo(a versions.GitOpsApp, latest string) string {
	if s.pulls == nil || latest == "" || a.Tag == "" || !strings.HasPrefix(versions.RepoWebURL(a.Repo), "https://github.com/") {
		return ""
	}
	running, ok1 := versions.ParseVersion(strings.TrimPrefix(a.Tag, "v"))
	next, ok2 := versions.ParseVersion(strings.TrimPrefix(latest, "v"))
	if !ok1 || !ok2 || next.Compare(running) <= 0 {
		return ""
	}
	return latest
}

// openGitOpsPR opens a pull request that pins the service's newest acceptable release in the file its Argo CD
// application deploys from.
func (s *Server) openGitOpsPR(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	ctx := r.Context()
	name := r.PathValue("name")
	path := "/services/" + url.PathEscape(name)
	svc, err := s.store.GetServiceByName(ctx, p.Scope, name)
	if err != nil {
		http.NotFound(w, r)
		return nil
	}
	if s.pulls == nil {
		return back(w, r, path, "error", "Set GOLIASH_GITOPS_TOKEN on the server to open pull requests.")
	}
	o, err := versions.LoadOverview(ctx, s.store, p.Scope)
	if err != nil {
		return err
	}
	apps, err := versions.GitOpsApps(ctx, s.store, p.Scope, o)
	if err != nil {
		return err
	}
	latest := ""
	if u, ok := o.Upstreams[svc.ID]; ok && u.HasLatest {
		latest = u.Latest.Raw
	}
	var app *versions.GitOpsApp
	for i := range apps {
		a := &apps[i]
		if a.ServiceID == svc.ID && a.TargetID == r.FormValue("target_id") && a.WorkloadID == r.FormValue("workload_id") {
			app = a
		}
	}
	to := r.FormValue("to")
	if app == nil || to == "" || s.pullRequestTo(*app, latest) != to {
		return back(w, r, path, "error", "That upgrade is no longer offered; the page is reloaded with what runs now.")
	}
	where := app.Path
	if app.TargetRevision != "" {
		where += " @ " + app.TargetRevision
	}
	body := fmt.Sprintf("Upgrades **%s** in **%s** from `%s` to `%s`, the newest release its version policy accepts.\n\n"+
		"- Argo CD application: `%s` (%s)\n- Image: `%s`\n- Opened by %s from [Goliash](%s%s)\n",
		svc.Name, app.Environment, app.Tag, to, app.App, where, versions.ParseImage(app.Image).Repo(), p.User.Email, s.publicURL, path)
	if notes := o.ReleaseURL[svc.ID+"|"+to]; notes != "" {
		body += "- Release notes: " + notes + "\n"
	}
	pr, err := s.pulls.OpenBump(ctx, gitops.Bump{
		Repo: versions.RepoWebURL(app.Repo), Branch: app.TargetRevision, Path: app.Path, Image: versions.ParseImage(app.Image).Repo(),
		From: app.Tag, To: to, Head: "goliash/" + branchSafe(svc.Name+"-"+to+"-"+app.Environment),
		Title: fmt.Sprintf("%s %s in %s", svc.Name, to, app.Environment), Body: body,
		CommitMsg: fmt.Sprintf("%s: %s → %s in %s", svc.Name, app.Tag, to, app.Environment),
	})
	switch {
	case errors.Is(err, gitops.ErrNoTag):
		return back(w, r, path, "error", fmt.Sprintf("No file under %s pins %s at %s.", app.Path, versions.ParseImage(app.Image).Repo(), app.Tag))
	case errors.Is(err, gitops.ErrPinned):
		return back(w, r, path, "error", "The repository pins the image by digest: change it there, with the new digest.")
	case err != nil:
		return back(w, r, path, "error", "The pull request could not be opened: "+err.Error())
	}
	s.audit(ctx, p, "gitops.pull_request", "service", svc.Name, "environment", app.Environment, "to", to, "url", pr)
	return back(w, r, path, "notice", "Pull request opened: "+pr)
}

// branchSafe makes a branch name of service, version and environment.
func branchSafe(s string) string {
	return strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '.' || r == '-' || r == '_' {
			return r
		}
		return '-'
	}, s)
}
