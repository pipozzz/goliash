// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package ui

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/pipozzz/goliash/internal/auth"
	"github.com/pipozzz/goliash/internal/gitops"
	"github.com/pipozzz/goliash/internal/notifier"
	"github.com/pipozzz/goliash/internal/store"
	"github.com/pipozzz/goliash/internal/versions"
)

// PullRequester opens an upgrade as a pull (or merge) request in a GitOps repository it supports.
type PullRequester interface {
	Supports(repo string) bool
	Plan(ctx context.Context, b gitops.Bump) (gitops.Plan, error)
	OpenBump(ctx context.Context, b gitops.Bump) (string, error)
	PullState(ctx context.Context, pullURL string) (string, error)
	Pinned(ctx context.Context, b gitops.Bump) ([]string, error)
	OpenPulls(ctx context.Context, repo string) ([]gitops.Pull, error)
	PullChecks(ctx context.Context, pullURL string) (string, error)
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

// upgrade is a pull request Goliash may open: an application, the version to pin, and why.
type upgrade struct {
	app            versions.GitOpsApp
	service, page  string
	to, why, notes string
	vulns          versions.VulnDelta
	licenses       versions.LicenseDelta // packages it brings under more demanding licenses
	summary        string                // Goli's summary of the release notes, a markdown list; empty without Goli
	rollback       bool                  // back to the version the running one replaced, the application being degraded
	reviewers      []string
	labels         []string // from the service's version policy (pr_reviewers, pr_labels)
}

// findUpgrade finds the upgrade a form names and checks the page would still offer it: the newest acceptable
// release, a promotion of the version that waits in the environment before, or a rollback of a degraded
// application to the version before.
func (s *Server) findUpgrade(ctx context.Context, sc store.Scope, service, targetID, workloadID, to string) (upgrade, bool, error) {
	o, err := versions.LoadOverview(ctx, s.store, sc)
	if err != nil {
		return upgrade{}, false, err
	}
	apps, err := versions.GitOpsApps(ctx, s.store, sc, o)
	if err != nil {
		return upgrade{}, false, err
	}
	var app *versions.GitOpsApp
	for i := range apps {
		a := &apps[i]
		if o.Services[a.ServiceID].Name == service && a.TargetID == targetID && a.WorkloadID == workloadID && s.canPR(*a) {
			app = a
		}
	}
	if app == nil || to == "" {
		return upgrade{}, false, nil
	}
	u := upgrade{
		app: *app, service: service, page: "/services/" + url.PathEscape(service), to: to,
		notes: o.ReleaseURL[app.ServiceID+"|"+to],
	}
	if pol, _, err := versions.PolicyFor(o.Services[app.ServiceID], o.Refs[app.ServiceID].Repo); err == nil {
		u.reviewers, u.labels = pol.PRReviewers, pol.PRLabels
	}
	if up, ok := o.Upstreams[app.ServiceID]; ok && up.HasLatest && s.pullRequestTo(*app, up.Latest.Raw) == to {
		u.why = "the newest release its version policy accepts"
	} else {
		list, err := versions.Promotions(ctx, s.store, sc, o)
		if err != nil {
			return upgrade{}, false, err
		}
		for _, pr := range list {
			if pr.Service.ID == app.ServiceID && pr.To.Name == app.Environment && pr.Version == to && pr.Running == app.Tag {
				u.why = fmt.Sprintf("it has run in %s since %s", pr.From.Name, pr.Since.UTC().Format("2 Jan 2006"))
			}
		}
	}
	if u.why == "" {
		if back, why := s.rollbackFor(ctx, sc, *app, time.Now()); back == to {
			u.why, u.rollback = why, true
		}
	}
	if u.why == "" {
		return upgrade{}, false, nil
	}
	u.vulns, _ = versions.UpgradeVulns(ctx, s.store, sc, app.ServiceID, envID(o, app.Environment), app.Tag, to)
	u.licenses, _ = versions.UpgradeLicenses(ctx, s.store, sc, app.ServiceID, envID(o, app.Environment), app.Tag, to)
	return u, true, nil
}

// openGitOpsPR opens the pull request a form names: an upgrade from a service page or the Updates page, or a
// promotion from the Delivery page.
func (s *Server) openGitOpsPR(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	ctx := r.Context()
	name := r.PathValue("name")
	path := "/services/" + url.PathEscape(name)
	if ret := r.FormValue("return"); ret == "/delivery" || ret == "/updates" || ret == "/security" || ret == "/gitops" {
		path = ret
	}
	if s.pulls == nil {
		return back(w, r, path, "error", "Set GOLIASH_GITOPS_TOKEN on the server to open pull requests.")
	}
	u, ok, err := s.findUpgrade(ctx, p.Scope, name, r.FormValue("target_id"), r.FormValue("workload_id"), r.FormValue("to"))
	if err != nil {
		return err
	}
	if !ok {
		return back(w, r, path, "error", "That upgrade is no longer offered; the page is reloaded with what runs now.")
	}
	if other, ok := s.othersPullNow(ctx, u.app, u.to); ok {
		return back(w, r, path, "notice", "Someone opened this upgrade already: "+other.URL)
	}
	pr, msg := s.bump(ctx, p.Scope, p.User.Email, u)
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

// bumpRequest is the pull request an upgrade opens: branch, title, commit and the body that says what, where, why,
// who and what it does to known vulnerabilities.
func (s *Server) bumpRequest(u upgrade, by string) gitops.Bump {
	app, image := u.app, versions.ParseImage(u.app.Image).Repo()
	where := app.Path
	if app.TargetRevision != "" {
		where += " @ " + app.TargetRevision
	}
	verb, title, head := "Upgrades", fmt.Sprintf("%s %s in %s", u.service, u.to, app.Environment), u.service+"-"+u.to+"-"+app.Environment
	if u.rollback {
		verb, title, head = "Rolls back", fmt.Sprintf("Roll back %s to %s in %s", u.service, u.to, app.Environment), "rollback-"+head
	}
	body := fmt.Sprintf("%s **%s** in **%s** from `%s` to `%s`: %s.\n\n"+
		"- %s: `%s` (%s)\n- Image: `%s`\n- Opened by %s from [Goliash](%s%s)\n",
		verb, u.service, app.Environment, app.Tag, u.to, u.why, versions.GitOpsObject(app.Tool), app.App, where, image, by, s.publicURL, u.page)
	if u.notes != "" {
		body += "- Release notes: " + u.notes + "\n"
	}
	body += vulnsMarkdown(u.vulns)
	body += licensesMarkdown(u.licenses)
	if u.summary != "" {
		body += "\n### What changes\n\n" + u.summary + "\n\nSummarised from the release notes by Goli, Goliash's assistant: read them before merging.\n"
	}
	return gitops.Bump{
		Repo: versions.RepoWebURL(app.Repo), Branch: app.TargetRevision, Path: app.Path, Image: image,
		From: app.Tag, To: u.to, Head: "goliash/" + branchSafe(head), Title: title, Body: body,
		Reviewers: u.reviewers, Labels: u.labels,
		CommitMsg: fmt.Sprintf("%s: %s → %s in %s", u.service, app.Tag, u.to, app.Environment),
	}
}

// bumpError says why a pull request could not be opened or previewed.
func bumpError(err error, b gitops.Bump) string {
	switch {
	case errors.Is(err, gitops.ErrNoTag):
		return fmt.Sprintf("No file under %s pins %s at %s.", b.Path, b.Image, b.From)
	case errors.Is(err, gitops.ErrPinned):
		return "The repository pins the image by digest: change it there, with the new digest."
	case errors.Is(err, gitops.ErrDemo):
		return "This is the demo: it shows the pull request it would open, and opens none."
	}
	return "The pull request could not be opened: " + err.Error()
}

// bump opens an upgrade's pull request, once: one opened before is returned as is. It returns the pull request's
// address, or a message for the person who asked.
func (s *Server) bump(ctx context.Context, sc store.Scope, by string, u upgrade) (string, string) {
	app := u.app
	if open, ok := s.openPull(ctx, sc, app, u.to); ok { // proposed already: never twice
		return open, ""
	}
	if other, ok := s.othersPullNow(ctx, app, u.to); ok { // Renovate, Dependabot or a person proposed it: not twice either
		return other.URL, ""
	}
	s.addSummary(ctx, sc, &u)
	req := s.bumpRequest(u, by)
	pr, err := s.pulls.OpenBump(ctx, req)
	if err != nil {
		return "", bumpError(err, req)
	}
	if err := s.store.RecordGitOpsPull(ctx, store.GitOpsPull{
		Scope: sc, ServiceID: app.ServiceID, Environment: app.Environment,
		TargetID: app.TargetID, WorkloadID: app.WorkloadID, From: app.Tag, To: u.to, URL: pr, OpenedBy: by,
	}); err != nil {
		s.log.ErrorContext(ctx, "recording a pull request failed", "url", pr, "err", err)
	}
	if s.notify != nil {
		owner := ""
		if svc, err := s.store.GetService(ctx, sc, app.ServiceID); err == nil {
			owner = svc.Owner
		}
		s.notify.PullRequestOpened(sc, notifier.Item{
			Service: u.service, Owner: owner, Environment: app.Environment,
			Target: app.Target, From: app.Tag, To: u.to, Note: by, URL: pr, Tool: app.Tool,
		})
	}
	if err := s.store.Audit(ctx, store.AuditEntry{
		OrgID: sc.OrgID, WorkspaceID: sc.WorkspaceID, Actor: by, Action: "gitops.pull_request",
		Details: map[string]string{"service": u.service, "environment": app.Environment, "to": u.to, "url": pr},
	}); err != nil {
		s.log.ErrorContext(ctx, "audit log write failed", "action", "gitops.pull_request", "err", err)
	}
	return pr, ""
}

// upgradePRs are the pull requests a page may offer to move a service in an environment from running to target,
// one per GitOps application that runs it there: when target is the newest acceptable release, as the service page
// offers it. Opened ones carry their address and state.
func (s *Server) upgradePRs(ctx context.Context, sc store.Scope, o versions.Overview, apps []versions.GitOpsApp, serviceID, env, running, target string) []PromotePR {
	up, ok := o.Upstreams[serviceID]
	if !ok || !up.HasLatest || target != up.Latest.Raw {
		return nil
	}
	var out []PromotePR
	for _, a := range apps {
		if a.ServiceID == serviceID && a.Environment == env && a.Tag == running && s.pullRequestTo(a, target) == target {
			pp := PromotePR{TargetID: a.TargetID, WorkloadID: a.WorkloadID, Target: a.Target, App: a.App}
			if open, ok := s.pullFor(ctx, sc, a, target); ok && open.State != "closed" {
				pp.URL, pp.State, pp.Checks = open.URL, open.State, open.Checks
			} else if other, ok := s.othersPull(a, target); ok {
				pp.URL, pp.State, pp.Title = other.URL, "open", other.Title
			}
			out = append(out, pp)
		}
	}
	return out
}

// pullFor is the pull request Goliash opened to move the application's workload to the version, whatever became of
// it.
func (s *Server) pullFor(ctx context.Context, sc store.Scope, app versions.GitOpsApp, to string) (store.GitOpsPull, bool) {
	pulls, err := s.store.ListGitOpsPulls(ctx, sc)
	if err != nil {
		return store.GitOpsPull{}, false
	}
	for _, p := range pulls {
		if p.TargetID == app.TargetID && p.WorkloadID == app.WorkloadID && p.To == to {
			return p, true
		}
	}
	return store.GitOpsPull{}, false
}

// openPull is the pull request Goliash opened for the version and nobody closed without merging: one closed may be
// opened again.
func (s *Server) openPull(ctx context.Context, sc store.Scope, app versions.GitOpsApp, to string) (string, bool) {
	p, ok := s.pullFor(ctx, sc, app, to)
	if !ok || p.State == "closed" {
		return "", false
	}
	return p.URL, true
}

// pullLabel is the link to an opened pull request, with what became of it.
func pullLabel(to, state string) string {
	label := "Pull request → " + to
	if state == "merged" {
		label += " · merged"
	}
	return label
}

// RefreshPulls asks the forges what became of the open pull requests Goliash opened.
func (s *Server) RefreshPulls(ctx context.Context) error {
	if s.pulls == nil {
		return nil
	}
	wss, err := s.store.ListWorkspaces(ctx)
	if err != nil {
		return err
	}
	for _, ws := range wss {
		sc := ws.Scope()
		pulls, err := s.store.ListGitOpsPulls(ctx, sc)
		if err != nil {
			return err
		}
		for _, p := range pulls {
			if p.State != "open" {
				continue
			}
			state, err := s.pulls.PullState(ctx, p.URL)
			if err != nil {
				s.log.WarnContext(ctx, "reading a pull request's state failed", "url", p.URL, "err", err)
				continue
			}
			if state != p.State {
				if err := s.store.SetGitOpsPullState(ctx, sc, p.ID, state); err != nil {
					return err
				}
			}
			if state != "open" {
				continue
			}
			checks, err := s.pulls.PullChecks(ctx, p.URL)
			if err != nil {
				s.log.WarnContext(ctx, "reading a pull request's checks failed", "url", p.URL, "err", err)
				continue
			}
			if checks == p.Checks {
				continue
			}
			if err := s.store.SetGitOpsPullChecks(ctx, sc, p.ID, checks); err != nil {
				return err
			}
			if checks == "failing" && s.notify != nil { // a red pull request nobody looks at helps nobody
				svc, _ := s.store.GetService(ctx, sc, p.ServiceID)
				s.notify.PullRequestFailing(sc, notifier.Item{
					Service: svc.Name, Owner: svc.Owner, Environment: p.Environment, From: p.From, To: p.To, URL: p.URL,
				})
			}
		}
	}
	return nil
}

// promotePR opens a promotion from the Delivery page as a pull request: the version that runs in the previous
// environment, pinned in the repository of the environment behind.
func (s *Server) promotePR(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	ctx := r.Context()
	const page = "/delivery"
	if s.pulls == nil {
		return back(w, r, page, "error", "Set GOLIASH_GITOPS_TOKEN on the server to open pull requests.")
	}
	u, ok, err := s.findUpgrade(ctx, p.Scope, r.FormValue("service"), r.FormValue("target_id"), r.FormValue("workload_id"), r.FormValue("version"))
	if err != nil {
		return err
	}
	if !ok || u.app.Environment != r.FormValue("env") {
		return back(w, r, page, "error", "That promotion is no longer waiting; the page is reloaded with what runs now.")
	}
	u.page = page
	url, msg := s.bump(ctx, p.Scope, p.User.Email, u)
	if msg != "" {
		return back(w, r, page, "error", msg)
	}
	return back(w, r, page, "notice", "Pull request opened: "+url)
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

// vulnsMarkdown says in a pull request what the upgrade does to known vulnerabilities, from the SBOMs of both
// images: the reviewer's best reason to merge, or to wait.
func vulnsMarkdown(d versions.VulnDelta) string {
	if !d.Known || len(d.Fixed)+len(d.New) == 0 {
		return ""
	}
	list := func(refs []versions.VulnRef) string {
		var parts []string
		for i, v := range refs {
			if i == 10 {
				parts = append(parts, fmt.Sprintf("and %d more", len(refs)-10))
				break
			}
			item := "`" + v.ID + "`"
			if v.Exploited {
				item += " (**exploited in the wild**)"
			} else if v.Severity != "" {
				item += " (" + strings.ToLower(v.Severity) + ")"
			}
			parts = append(parts, item)
		}
		return strings.Join(parts, ", ")
	}
	var b strings.Builder
	b.WriteString("\n### Known vulnerabilities\n\n")
	if len(d.Fixed) > 0 {
		head := fmt.Sprintf("Fixes %d", len(d.Fixed))
		if n := versions.Exploited(d.Fixed); n > 0 {
			head += fmt.Sprintf(", %d of them exploited in the wild", n)
		}
		b.WriteString("- " + head + ": " + list(d.Fixed) + "\n")
	}
	if len(d.New) > 0 {
		fmt.Fprintf(&b, "- Brings %d new: %s\n", len(d.New), list(d.New))
	}
	b.WriteString("\nFrom the SBOMs of both images and OSV, as Goliash reads them.\n")
	return b.String()
}

// envID is the ID of the environment named name.
func envID(o versions.Overview, name string) string {
	for id, e := range o.Envs {
		if e.Name == name {
			return id
		}
	}
	return ""
}

// pullViews are the pull requests Goliash opened in the last 30 days, newest first, with whether the version each
// pins runs where it was meant to.
func (s *Server) pullViews(ctx context.Context, sc store.Scope, o versions.Overview, apps []versions.GitOpsApp) ([]PullView, error) {
	pulls, err := s.store.ListGitOpsPulls(ctx, sc)
	if err != nil {
		return nil, err
	}
	running := map[string]string{} // target|workload -> tag
	targetName := map[string]string{}
	for _, a := range apps {
		running[a.TargetID+"|"+a.WorkloadID] = a.Tag
		targetName[a.TargetID] = a.Target
	}
	var out []PullView
	arrived := map[string][]store.Event{} // service -> its version changes since the oldest pull request shown
	for _, p := range pulls {
		if time.Since(p.OpenedAt) > 30*24*time.Hour {
			continue
		}
		name := o.Services[p.ServiceID].Name
		v := PullView{
			Service: name, ServiceURL: "/services/" + url.PathEscape(name), Environment: p.Environment, Target: targetName[p.TargetID],
			From: p.From, To: p.To, URL: p.URL, State: p.State, Checks: p.Checks, OpenedBy: p.OpenedBy, OpenedAt: p.OpenedAt,
			Deployed: running[p.TargetID+"|"+p.WorkloadID] == p.To,
		}
		if v.Deployed {
			evs, ok := arrived[p.ServiceID]
			if !ok {
				evs, _ = s.store.ListEvents(ctx, sc, store.EventFilter{
					ServiceID: p.ServiceID, Types: []string{"version_changed", "deployed"}, Since: time.Now().Add(-31 * 24 * time.Hour), Limit: 1000,
				})
				arrived[p.ServiceID] = evs
			}
			for _, e := range evs { // newest first: keep the first arrival after the pull request was opened
				if e.TargetID == p.TargetID && e.ToVersion == p.To && !e.At.Before(p.OpenedAt) {
					v.ToRunning = e.At.Sub(p.OpenedAt)
				}
			}
		}
		out = append(out, v)
	}
	return out, nil
}

// medianToRunning is the median time from opening a pull request to its version running, over those that run,
// and how many; 0 when none does.
func medianToRunning(pulls []PullView) (time.Duration, int) {
	var ds []time.Duration
	for _, p := range pulls {
		if p.ToRunning > 0 {
			ds = append(ds, p.ToRunning)
		}
	}
	if len(ds) == 0 {
		return 0, 0
	}
	slices.Sort(ds)
	return ds[len(ds)/2], len(ds)
}

// pullStateClass is the badge of a pull request's state.
func pullStateClass(state string) string {
	switch state {
	case "merged":
		return "ok"
	case "closed":
		return "neutral"
	}
	return "upstream"
}

// bulkMax bounds how many pull requests one click opens.
const bulkMax = 20

// bulkValue names one pull request for the Updates page's ticks: service, target, workload and version.
func bulkValue(service string, pr PromotePR, to string) string {
	return strings.Join([]string{service, pr.TargetID, pr.WorkloadID, to}, "|")
}

// openBulkPRs opens the pull requests ticked on the Updates page, each as a click on its Open PR would: what the
// page no longer offers, or was opened already, is skipped.
func (s *Server) openBulkPRs(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	ctx := r.Context()
	ret := r.FormValue("return")
	if !strings.HasPrefix(ret, "/updates") || strings.HasPrefix(ret, "//") {
		ret = "/updates"
	}
	if s.pulls == nil || s.demoPulls() {
		return back(w, r, ret, "error", "Set a GitOps token on the server to open pull requests.")
	}
	if err := r.ParseForm(); err != nil {
		return err
	}
	picks := r.PostForm["pr"]
	if len(picks) == 0 {
		return back(w, r, ret, "error", "Tick the upgrades to open as pull requests first.")
	}
	if len(picks) > bulkMax {
		return back(w, r, ret, "error", fmt.Sprintf("At most %d at a time.", bulkMax))
	}
	var opened, failed []string
	var ups []upgrade
	for _, pick := range picks {
		i := strings.LastIndex(pick, "|") // service|target|workload|version; the service's name may hold a "|"
		head, to := pick[:max(i, 0)], pick[i+1:]
		j := strings.LastIndex(head, "|")
		k := strings.LastIndex(head[:max(j, 0)], "|")
		if i < 0 || j < 0 || k < 0 {
			continue
		}
		f := []string{head[:k], head[k+1 : j], head[j+1:], to}
		u, ok, err := s.findUpgrade(ctx, p.Scope, f[0], f[1], f[2], f[3])
		if err != nil {
			return err
		}
		if !ok {
			failed = append(failed, f[0]+" "+f[3]+" (no longer offered)")
			continue
		}
		ups = append(ups, u)
	}
	// One pull request per repository, branch and environment when asked; each on its own otherwise.
	groups := map[string][]upgrade{}
	var order []string
	for _, u := range ups {
		k := u.service + "|" + u.app.TargetID + "|" + u.app.WorkloadID
		if r.PostForm.Get("group") == "repo" {
			_, open := s.openPull(ctx, p.Scope, u.app, u.to)
			if _, theirs := s.othersPullNow(ctx, u.app, u.to); !open && !theirs { // proposed already: on its own, linked
				k = versions.RepoWebURL(u.app.Repo) + "|" + u.app.TargetRevision + "|" + u.app.Environment
			}
		}
		if _, ok := groups[k]; !ok {
			order = append(order, k)
		}
		groups[k] = append(groups[k], u)
	}
	for _, k := range order {
		items := groups[k]
		if len(items) == 1 {
			u := items[0]
			if _, msg := s.bump(ctx, p.Scope, p.User.Email, u); msg != "" {
				failed = append(failed, u.service+" "+u.to+" ("+strings.TrimSuffix(msg, ".")+")")
				continue
			}
			opened = append(opened, u.service+" "+u.to+" in "+u.app.Environment)
			continue
		}
		var names []string
		for _, u := range items {
			names = append(names, u.service+" "+u.to)
		}
		env := items[0].app.Environment
		sum := sha256.Sum256([]byte(strings.Join(names, ",")))
		req := s.groupBump(items, p.User.Email,
			fmt.Sprintf("Upgrade %d services in %s: %s", len(items), env, strings.Join(names, ", ")),
			"upgrade-"+env+"-"+hex.EncodeToString(sum[:4]),
			fmt.Sprintf("Upgrades **%d services** in **%s** to the newest releases their version policies accept", len(items), env),
			"/updates")
		if _, msg := s.openGroup(ctx, p.Scope, p.User.Email, req, items, fmt.Sprintf("%d services", len(items))); msg != "" {
			failed = append(failed, strings.Join(names, ", ")+" ("+strings.TrimSuffix(msg, ".")+")")
			continue
		}
		opened = append(opened, strings.Join(names, ", ")+" in "+env+" (one pull request)")
	}
	msg := fmt.Sprintf("Opened %d pull requests: %s.", len(opened), strings.Join(opened, ", "))
	if len(opened) == 1 {
		msg = "Opened the pull request for " + opened[0] + "."
	}
	if len(failed) > 0 {
		msg += " Not opened: " + strings.Join(failed, ", ") + "."
		if len(opened) == 0 {
			return back(w, r, ret, "error", strings.TrimPrefix(msg, "Opened 0 pull requests: . "))
		}
	}
	return back(w, r, ret, "notice", msg)
}

// commaList reads "a, b,c" into its items, blanks left out.
func commaList(s string) []string {
	var out []string
	for _, f := range strings.Split(s, ",") {
		if f = strings.TrimSpace(f); f != "" && len(f) <= 100 {
			out = append(out, f)
		}
	}
	return out
}

// licenseClassNames say what a license class asks, for a reviewer.
var licenseClassNames = map[versions.LicenseClass]string{
	versions.NetworkCopyleft: "network copyleft", versions.SourceAvailable: "source-available",
	versions.StrongCopyleft: "strong copyleft",
}

// licenseList names license changes for a line: "`minio-go` 7.1 under **AGPL-3.0** (network copyleft)", at most n.
func licenseList(changes []versions.LicenseChange, n int) string {
	var parts []string
	for i, c := range changes {
		if i == n {
			parts = append(parts, fmt.Sprintf("and %d more", len(changes)-n))
			break
		}
		lic := c.License
		if lic == "" {
			lic = "an unknown license"
		}
		item := fmt.Sprintf("`%s` %s under **%s** (%s", c.Name, c.Version, lic, licenseClassNames[c.Class])
		if c.Was != "" {
			item += ", was " + c.Was
		}
		parts = append(parts, item+")")
	}
	return strings.Join(parts, ", ")
}

// licensesMarkdown says in a pull request which packages the upgrade brings, or relicenses, under terms that need
// a look before merging: network copyleft, source-available, strong copyleft. Nothing when there are none or the
// SBOMs are not known.
func licensesMarkdown(d versions.LicenseDelta) string {
	if !d.Known || len(d.Changes) == 0 {
		return ""
	}
	return "\n### Licenses\n\nBrings " + licenseList(d.Changes, 10) +
		".\n\nFrom the SBOMs of both images, as Goliash reads them; not legal advice.\n"
}

// licenseShort says a promotion's license changes in a few words: "AGPL-3.0-only (itextpdf) and 1 more".
func licenseShort(changes []versions.LicenseChange) string {
	c := changes[0]
	name := c.Name[strings.LastIndex(c.Name, "/")+1:]
	s := orUnknown(c.License) + " (" + name + ")"
	if len(changes) > 1 {
		s += fmt.Sprintf(" and %d more", len(changes)-1)
	}
	return s
}

// licensePlain lists license changes for a tooltip.
func licensePlain(changes []versions.LicenseChange) string {
	var parts []string
	for _, c := range changes {
		parts = append(parts, c.Name+" "+c.Version+" "+orUnknown(c.License))
	}
	return strings.Join(parts, ", ")
}

func orUnknown(license string) string {
	if license == "" {
		return "an unknown license"
	}
	return license
}
