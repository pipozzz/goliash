// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package ui

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"

	"github.com/pipozzz/goliash/internal/auth"
	"github.com/pipozzz/goliash/internal/gitops"
	"github.com/pipozzz/goliash/internal/notifier"
	"github.com/pipozzz/goliash/internal/store"
	"github.com/pipozzz/goliash/internal/versions"
)

// appPromotion is a whole application promoted at once: the services one GitOps application deploys in the
// environment behind, each waiting with the version that runs in the environment before, opened as one pull request.
type appPromotion struct {
	App, Target, TargetID string
	From, To              string // the environments
	items                 []upgrade
}

// key names an application promotion in links and forms.
func (a appPromotion) key() url.Values {
	return url.Values{"app": {a.App}, "target_id": {a.TargetID}, "from": {a.From}, "to": {a.To}}
}

// appPromotions groups the promotions waiting by the GitOps application that deploys them in the environment
// behind (same repository and branch); those of two services or more can go as one pull request. Services with a
// pull request open for the version (Goliash's or someone else's) are left out.
func (s *Server) appPromotions(ctx context.Context, sc store.Scope, o versions.Overview, list []versions.Promotion, apps []versions.GitOpsApp) []appPromotion {
	if s.pulls == nil {
		return nil
	}
	byKey := map[string]*appPromotion{}
	var keys []string
	for _, pr := range list {
		for _, a := range promotionApps(apps, pr, s.canPR) {
			if open, ok := s.pullFor(ctx, sc, a, pr.Version); ok && open.State != "closed" {
				continue
			}
			if _, theirs := s.othersPull(a, pr.Version); theirs {
				continue
			}
			k := strings.Join([]string{a.App, a.TargetID, versions.RepoWebURL(a.Repo), a.TargetRevision, pr.From.Name, pr.To.Name}, "|")
			g, ok := byKey[k]
			if !ok {
				g = &appPromotion{App: a.App, Target: a.Target, TargetID: a.TargetID, From: pr.From.Name, To: pr.To.Name}
				byKey[k] = g
				keys = append(keys, k)
			}
			u := upgrade{
				app: a, service: pr.Service.Name, page: "/delivery", to: pr.Version, notes: o.ReleaseURL[a.ServiceID+"|"+pr.Version],
				why: fmt.Sprintf("it has run in %s since %s", pr.From.Name, pr.Since.UTC().Format("2 Jan 2006")),
			}
			u.vulns, _ = versions.UpgradeVulns(ctx, s.store, sc, a.ServiceID, envID(o, a.Environment), a.Tag, pr.Version)
			g.items = append(g.items, u)
		}
	}
	var out []appPromotion
	for _, k := range keys {
		if g := byKey[k]; len(g.items) > 1 {
			sort.Slice(g.items, func(i, j int) bool { return g.items[i].service < g.items[j].service })
			out = append(out, *g)
		}
	}
	return out
}

// findAppPromotion is the application promotion a link or form names, if the page still offers it.
func (s *Server) findAppPromotion(ctx context.Context, sc store.Scope, q url.Values) (appPromotion, bool, error) {
	o, err := versions.LoadOverview(ctx, s.store, sc)
	if err != nil {
		return appPromotion{}, false, err
	}
	list, err := versions.Promotions(ctx, s.store, sc, o)
	if err != nil {
		return appPromotion{}, false, err
	}
	apps, err := versions.GitOpsApps(ctx, s.store, sc, o)
	if err != nil {
		return appPromotion{}, false, err
	}
	for _, g := range s.appPromotions(ctx, sc, o, list, apps) {
		if g.App == q.Get("app") && g.TargetID == q.Get("target_id") && g.From == q.Get("from") && g.To == q.Get("to") {
			return g, true, nil
		}
	}
	return appPromotion{}, false, nil
}

// promotionBump is the pull request that promotes a whole application: every service's tag, in one branch.
func (s *Server) promotionBump(g appPromotion, by string) gitops.Bump {
	var names []string
	for _, u := range g.items {
		names = append(names, u.service+" "+u.to)
	}
	return s.groupBump(g.items, by,
		fmt.Sprintf("Promote %s to %s: %s", g.App, g.To, strings.Join(names, ", ")),
		"promote-"+g.App+"-"+g.To,
		fmt.Sprintf("Promotes **%d services** of `%s` in **%s** (%s) to the versions that run in **%s**", len(g.items), g.App, g.To, g.Target, g.From),
		"/delivery")
}

// groupBump is one pull request for several upgrades in one repository and branch: the first upgrade's, carrying
// the others' pins, with a line per service in its description.
func (s *Server) groupBump(items []upgrade, by, title, head, intro, page string) gitops.Bump {
	b := s.bumpRequest(items[0], by)
	var body strings.Builder
	body.WriteString(intro + ":\n\n")
	for i, u := range items {
		fmt.Fprintf(&body, "- **%s** `%s` → `%s`: %s", u.service, u.app.Tag, u.to, u.why)
		if u.notes != "" {
			fmt.Fprintf(&body, " ([release notes](%s))", u.notes)
		}
		if d := u.vulns; d.Known && len(d.Fixed)+len(d.New) > 0 {
			fmt.Fprintf(&body, "; fixes %d known vulnerabilities", len(d.Fixed))
			if n := versions.Exploited(d.Fixed); n > 0 {
				fmt.Fprintf(&body, " (%d exploited in the wild)", n)
			}
			if len(d.New) > 0 {
				fmt.Fprintf(&body, ", brings %d", len(d.New))
			}
		}
		body.WriteString("\n")
		if i > 0 {
			b.Also = append(b.Also, gitops.Pin{Path: u.app.Path, Image: versions.ParseImage(u.app.Image).Repo(), From: u.app.Tag, To: u.to})
		}
	}
	where := items[0].app.TargetRevision
	if where == "" {
		where = "the default branch"
	}
	fmt.Fprintf(&body, "\n- Repository: %s (%s)\n- Opened by %s from [Goliash](%s%s)\n", b.Repo, where, by, s.publicURL, page)
	b.Head = "goliash/" + branchSafe(head)
	b.Title = title
	b.CommitMsg = title
	b.Body = body.String()
	return b
}

// openGroup opens one pull request for several upgrades and records it for each: every service's page links it.
// It returns the pull request's address, or why it could not be opened.
func (s *Server) openGroup(ctx context.Context, sc store.Scope, by string, req gitops.Bump, items []upgrade, label string) (string, string) {
	pr, err := s.pulls.OpenBump(ctx, req)
	if err != nil {
		return "", bumpError(err, req)
	}
	var names []string
	for _, u := range items {
		names = append(names, u.service)
		if err := s.store.RecordGitOpsPull(ctx, store.GitOpsPull{
			Scope: sc, ServiceID: u.app.ServiceID, Environment: u.app.Environment, TargetID: u.app.TargetID,
			WorkloadID: u.app.WorkloadID, From: u.app.Tag, To: u.to, URL: pr, OpenedBy: by,
		}); err != nil {
			s.log.ErrorContext(ctx, "recording a pull request failed", "url", pr, "err", err)
		}
	}
	if s.notify != nil {
		s.notify.PullRequestOpened(sc, notifier.Item{
			Service: label, Environment: items[0].app.Environment, Target: items[0].app.Target, From: items[0].app.Tag,
			To: items[0].to, Note: by, URL: pr, Tool: items[0].app.Tool,
		})
	}
	if err := s.store.Audit(ctx, store.AuditEntry{
		OrgID: sc.OrgID, WorkspaceID: sc.WorkspaceID, Actor: by, Action: "gitops.pull_request",
		Details: map[string]string{"group": label, "environment": items[0].app.Environment, "services": strings.Join(names, ","), "url": pr},
	}); err != nil {
		s.log.ErrorContext(ctx, "audit log write failed", "action", "gitops.pull_request", "err", err)
	}
	return pr, ""
}

// previewAppPromotion shows the pull request that would promote a whole application.
func (s *Server) previewAppPromotion(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	ctx := r.Context()
	if s.pulls == nil || !p.Can(store.RoleMember) && !s.demoPulls() {
		http.NotFound(w, r)
		return nil
	}
	g, ok, err := s.findAppPromotion(ctx, p.Scope, r.URL.Query())
	if err != nil {
		return err
	}
	if !ok {
		return back(w, r, "/delivery", "error", "That promotion is no longer waiting; the page is reloaded with what runs now.")
	}
	by := p.User.Email
	if s.demoPulls() {
		by = "you"
	}
	req := s.promotionBump(g, by)
	v := GitOpsPreviewView{
		Base: s.base(ctx, p, "promotions", "Pull request preview"), Service: "Delivery", ServiceURL: "/delivery",
		Repo: strings.TrimPrefix(req.Repo, "https://"), RepoURL: req.Repo, Head: req.Head, Title: req.Title, Body: req.Body,
		OpenURL: "/delivery/promote-app", CanOpen: p.Can(store.RoleMember) && !s.demoPulls(),
	}
	for k, vals := range g.key() {
		v.Fields = append(v.Fields, [2]string{k, vals[0]})
	}
	sort.Slice(v.Fields, func(i, j int) bool { return v.Fields[i][0] < v.Fields[j][0] })
	plan, err := s.pulls.Plan(ctx, req)
	if err != nil {
		v.Problem, v.CanOpen = bumpError(err, req), false
	}
	v.From = plan.Base
	for _, c := range plan.Changes {
		v.Files = append(v.Files, PreviewFile{Path: c.Path, Lines: diffLines(string(c.Old), string(c.New), 2)})
	}
	return render(w, r, GitOpsPreviewPage(v))
}

// openAppPromotion opens the pull request that promotes a whole application, and records it for every service in
// it: each service's page links it.
func (s *Server) openAppPromotion(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	ctx := r.Context()
	const page = "/delivery"
	if s.pulls == nil || s.demoPulls() {
		return back(w, r, page, "error", "Set a GitOps token on the server to open pull requests.")
	}
	if err := r.ParseForm(); err != nil {
		return err
	}
	g, ok, err := s.findAppPromotion(ctx, p.Scope, r.PostForm)
	if err != nil {
		return err
	}
	if !ok {
		return back(w, r, page, "error", "That promotion is no longer waiting; the page is reloaded with what runs now.")
	}
	pr, msg := s.openGroup(ctx, p.Scope, p.User.Email, s.promotionBump(g, p.User.Email), g.items, g.App)
	if msg != "" {
		return back(w, r, page, "error", msg)
	}
	var names []string
	for _, u := range g.items {
		names = append(names, u.service)
	}
	return back(w, r, page, "notice", fmt.Sprintf("Pull request opened for %s (%s): %s", g.App, strings.Join(names, ", "), pr))
}

// joinList is a list for a line: "a, b and c".
func joinList(items []string) string {
	switch len(items) {
	case 0:
		return ""
	case 1:
		return items[0]
	}
	return strings.Join(items[:len(items)-1], ", ") + " and " + items[len(items)-1]
}
