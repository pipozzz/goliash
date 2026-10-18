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

// PullRequester opens an upgrade as a pull (or merge) request in a GitOps repository it supports.
type PullRequester interface {
	Supports(repo string) bool
	OpenBump(ctx context.Context, b gitops.Bump) (string, error)
}

// pullRequestTo is the version a pull request for the application would pin: the newest acceptable release, when
// it is newer than what runs and the application deploys from a GitHub repository; else "".
func (s *Server) pullRequestTo(a versions.GitOpsApp, latest string) string {
	if !s.canPR(a) || latest == "" {
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
	why := "the newest release its version policy accepts"
	pr, msg := s.bump(ctx, p, svc.Name, path, *app, to, why, o.ReleaseURL[svc.ID+"|"+to])
	if msg != "" {
		return back(w, r, path, "error", msg)
	}
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

// canPR says whether an upgrade of the application can be opened as a pull request: a token, and a GitHub
// repository to open it in.
func (s *Server) canPR(a versions.GitOpsApp) bool {
	return s.pulls != nil && a.Tag != "" && s.pulls.Supports(versions.RepoWebURL(a.Repo))
}

// bump opens the pull request that pins to in the application's repository; why says why this version, notes is
// a release notes address. It returns the pull request's address, or a message for the person who asked.
func (s *Server) bump(ctx context.Context, p auth.Principal, service, page string, app versions.GitOpsApp, to, why, notes string) (string, string) {
	image := versions.ParseImage(app.Image).Repo()
	where := app.Path
	if app.TargetRevision != "" {
		where += " @ " + app.TargetRevision
	}
	body := fmt.Sprintf("Upgrades **%s** in **%s** from `%s` to `%s`: %s.\n\n"+
		"- %s: `%s` (%s)\n- Image: `%s`\n- Opened by %s from [Goliash](%s%s)\n",
		service, app.Environment, app.Tag, to, why, versions.GitOpsObject(app.Tool), app.App, where, image, p.User.Email, s.publicURL, page)
	if notes != "" {
		body += "- Release notes: " + notes + "\n"
	}
	pr, err := s.pulls.OpenBump(ctx, gitops.Bump{
		Repo: versions.RepoWebURL(app.Repo), Branch: app.TargetRevision, Path: app.Path, Image: image,
		From: app.Tag, To: to, Head: "goliash/" + branchSafe(service+"-"+to+"-"+app.Environment),
		Title: fmt.Sprintf("%s %s in %s", service, to, app.Environment), Body: body,
		CommitMsg: fmt.Sprintf("%s: %s → %s in %s", service, app.Tag, to, app.Environment),
	})
	switch {
	case errors.Is(err, gitops.ErrNoTag):
		return "", fmt.Sprintf("No file under %s pins %s at %s.", app.Path, image, app.Tag)
	case errors.Is(err, gitops.ErrPinned):
		return "", "The repository pins the image by digest: change it there, with the new digest."
	case err != nil:
		return "", "The pull request could not be opened: " + err.Error()
	}
	s.audit(ctx, p, "gitops.pull_request", "service", service, "environment", app.Environment, "to", to, "url", pr)
	return pr, ""
}

// promotePR opens a promotion from the Delivery page as a pull request: the version that runs in the previous
// environment, pinned in the repository of the environment behind.
func (s *Server) promotePR(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	ctx := r.Context()
	const page = "/delivery"
	if s.pulls == nil {
		return back(w, r, page, "error", "Set GOLIASH_GITOPS_TOKEN on the server to open pull requests.")
	}
	o, err := versions.LoadOverview(ctx, s.store, p.Scope)
	if err != nil {
		return err
	}
	list, err := versions.Promotions(ctx, s.store, p.Scope, o)
	if err != nil {
		return err
	}
	apps, err := versions.GitOpsApps(ctx, s.store, p.Scope, o)
	if err != nil {
		return err
	}
	for _, pr := range list {
		if pr.Service.Name != r.FormValue("service") || pr.To.Name != r.FormValue("env") || pr.Version != r.FormValue("version") {
			continue
		}
		for _, a := range promotionApps(apps, pr, s.canPR) {
			if a.TargetID != r.FormValue("target_id") || a.WorkloadID != r.FormValue("workload_id") {
				continue
			}
			why := fmt.Sprintf("it has run in %s since %s", pr.From.Name, pr.Since.UTC().Format("2 Jan 2006"))
			url, msg := s.bump(ctx, p, pr.Service.Name, page, a, pr.Version, why, o.ReleaseURL[pr.Service.ID+"|"+pr.Version])
			if msg != "" {
				return back(w, r, page, "error", msg)
			}
			return back(w, r, page, "notice", "Pull request opened: "+url)
		}
	}
	return back(w, r, page, "error", "That promotion is no longer waiting; the page is reloaded with what runs now.")
}

// promotionApps are the Argo CD applications a promotion would change: the service's, in the environment behind,
// running the version there.
func promotionApps(apps []versions.GitOpsApp, pr versions.Promotion, can func(versions.GitOpsApp) bool) []versions.GitOpsApp {
	var out []versions.GitOpsApp
	for _, a := range apps {
		if a.ServiceID == pr.Service.ID && a.Environment == pr.To.Name && a.Tag == pr.Running && can(a) {
			out = append(out, a)
		}
	}
	return out
}
