// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package ui

import (
	"context"
	"encoding/json"
	"html"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/pipozzz/goliash/internal/gitops"
	"github.com/pipozzz/goliash/internal/store"
	"github.com/pipozzz/goliash/internal/versions"
)

// A workload deployed by an Argo CD application: the service page says where from and which commit; out of
// sync for a while, the matrix cell says so.
func TestArgoCDOnServiceAndMatrix(t *testing.T) {
	e := newUIEnv(t)
	ctx := context.Background()
	sc := e.ws.Scope()
	e.ui.argoCDURL = "https://argo.example.com/"
	g := store.WorkloadGitOps{
		WorkloadID: "w1", Tool: "argocd", App: "evil-prod", AppNamespace: "argocd", Project: "shop",
		Repo: "git@github.com:acme/deploy.git", Path: "apps/evil/prod", TargetRevision: "main",
		Revision: "4f1c2e9d0b7a6c5e4f3a2b1c0d9e8f7a6b5c4d3e", Sync: "OutOfSync", Health: "Healthy",
	}
	if err := e.st.SetWorkloadGitOps(ctx, sc, e.tgt.ID, []store.WorkloadGitOps{g}, true, time.Now().Add(-2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	viewer := e.as(store.RoleViewer)
	svc := e.serviceOf("evil")
	_, page := get(t, viewer, e.srv.URL+"/services/"+url.PathEscape(svc), nil)
	for _, want := range []string{
		`<h2>Argo CD</h2>`, `href="https://argo.example.com/applications/argocd/evil-prod"`,
		`href="https://github.com/acme/deploy"`, `github.com/acme/deploy</a>`, `apps/evil/prod`, ` @ main`,
		`href="https://github.com/acme/deploy/commit/4f1c2e9d0b7a6c5e4f3a2b1c0d9e8f7a6b5c4d3e"`, `>4f1c2e9</a>`,
		`<span class="badge warn">OutOfSync</span>`, `<span class="badge ok">Healthy</span>`,
	} {
		if !strings.Contains(page, want) {
			t.Errorf("service page misses %q", want)
		}
	}
	_, matrix := get(t, viewer, e.srv.URL+"/?view=table", nil)
	if !strings.Contains(matrix, "Argo: out of sync") || !strings.Contains(matrix, "evil-prod on k8s-prod: OutOfSync, Healthy for 2h") {
		t.Errorf("matrix has no Argo CD badge")
	}

	// Back in sync: no badge, and the state's age starts again.
	g.Sync = "Synced"
	if err := e.st.SetWorkloadGitOps(ctx, sc, e.tgt.ID, []store.WorkloadGitOps{g}, true, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, matrix = get(t, viewer, e.srv.URL+"/?view=table", nil); strings.Contains(matrix, "Argo: ") {
		t.Error("an application in sync is badged")
	}
}

type fakePulls struct {
	got    []gitops.Bump
	state  string
	pinned []string
	others []gitops.Pull
	checks string
	// mergedAt is when the forge says it was merged
	mergedAt time.Time
}

func (f *fakePulls) Supports(repo string) bool { return strings.HasPrefix(repo, "https://github.com/") }

func (f *fakePulls) PullState(context.Context, string) (gitops.PullStatus, error) {
	return gitops.PullStatus{State: f.state, MergedAt: f.mergedAt}, nil
}

func (f *fakePulls) Pinned(context.Context, gitops.Bump) ([]string, error) { return f.pinned, nil }

func (f *fakePulls) OpenPulls(context.Context, string) ([]gitops.Pull, error) { return f.others, nil }

func (f *fakePulls) PullChecks(context.Context, string) (string, error) { return f.checks, nil }

func (f *fakePulls) Plan(ctx context.Context, b gitops.Bump) (gitops.Plan, error) {
	return gitops.Demo{}.Plan(ctx, b)
}

func (f *fakePulls) OpenBump(_ context.Context, b gitops.Bump) (string, error) {
	f.got = append(f.got, b)
	return "https://github.com/acme/deploy/pull/7", nil
}

// With a token, a member opens the upgrade to the newest acceptable release as a pull request in the repository the
// Argo CD application deploys from.
func TestOpenGitOpsPR(t *testing.T) {
	e := newUIEnv(t)
	ctx := context.Background()
	sc := e.ws.Scope()
	pulls := &fakePulls{}
	e.ui.pulls = pulls
	g := store.WorkloadGitOps{
		WorkloadID: "w1", Tool: "argocd", App: "evil-prod", Repo: "https://github.com/acme/deploy.git",
		Path: "apps/evil/prod", TargetRevision: "main", Revision: "abc1234", Sync: "Synced", Health: "Healthy",
	}
	if err := e.st.SetWorkloadGitOps(ctx, sc, e.tgt.ID, []store.WorkloadGitOps{g}, true, time.Now()); err != nil {
		t.Fatal(err)
	}
	svc := e.serviceOf("evil")
	o, _ := versions.LoadOverview(ctx, e.st, sc)
	apps, _ := versions.GitOpsApps(ctx, e.st, sc, o)
	if len(apps) != 1 {
		t.Fatalf("apps %+v", apps)
	}
	if e.ui.pullRequestTo(apps[0], "") != "" || e.ui.pullRequestTo(apps[0], apps[0].Tag) != "" {
		t.Fatal("offered without a newer release")
	}
	newer := "999.0.0"
	if got := e.ui.pullRequestTo(apps[0], newer); got != newer {
		t.Fatalf("pullRequestTo = %q", got)
	}
	// The form only accepts what the page would offer: here nothing newer is known upstream.
	member := e.as(store.RoleMember)
	_, body, _ := post(t, member, e.srv.URL+"/services/"+url.PathEscape(svc)+"/gitops/pr",
		url.Values{"target_id": {e.tgt.ID}, "workload_id": {"w1"}, "to": {newer}})
	if !strings.Contains(body, "no longer offered") || len(pulls.got) != 0 {
		t.Fatalf("an upgrade not offered was opened: %v", pulls.got)
	}

	// A newer release upstream: the page offers it, and the pull request goes to the application's repository.
	svcRec, _ := e.st.GetServiceByName(ctx, sc, svc)
	if _, err := e.st.InsertReleases(ctx, sc, svcRec.ID, []string{newer}); err != nil {
		t.Fatal(err)
	}
	if _, page := get(t, member, e.srv.URL+"/services/"+url.PathEscape(svc), nil); !strings.Contains(page, "Open PR → "+newer) {
		t.Fatal("no Open PR button")
	}
	viewer := e.as(store.RoleViewer)
	if _, page := get(t, viewer, e.srv.URL+"/services/"+url.PathEscape(svc), nil); strings.Contains(page, "Open PR") {
		t.Fatal("a viewer is offered a pull request")
	}
	_ = e.ui.checker.EvaluateDrift(ctx, sc) // the Updates page lists upstream drift (already recorded: fine)
	if _, page := get(t, member, e.srv.URL+"/updates", nil); !strings.Contains(page, "/gitops/preview?") || !strings.Contains(page, "to="+newer) {
		t.Fatal("no Open PR on the Updates page")
	}
	if _, page := get(t, viewer, e.srv.URL+"/updates", nil); strings.Contains(page, "/gitops/preview") {
		t.Fatal("a viewer is offered a pull request on the Updates page")
	}
	if _, page := get(t, member, e.srv.URL+"/security?env=all", nil); !strings.Contains(page, "return=%2Fsecurity") || !strings.Contains(page, "to="+newer) {
		t.Fatal("no Open PR on the Security page")
	}
	if _, page := get(t, viewer, e.srv.URL+"/security?env=all", nil); strings.Contains(page, "/gitops/preview") {
		t.Fatal("a viewer is offered a pull request on the Security page")
	}
	_, body, _ = post(t, member, e.srv.URL+"/services/"+url.PathEscape(svc)+"/gitops/pr",
		url.Values{"target_id": {e.tgt.ID}, "workload_id": {"w1"}, "to": {newer}})
	if !strings.Contains(body, "Pull request opened: https://github.com/acme/deploy/pull/7") || len(pulls.got) != 1 {
		t.Fatalf("not opened: %d", len(pulls.got))
	}
	b := pulls.got[0]
	if b.Repo != "https://github.com/acme/deploy" || b.Branch != "main" || b.Path != "apps/evil/prod" || b.From != apps[0].Tag ||
		b.To != newer || !strings.HasPrefix(b.Head, "goliash/") || !strings.Contains(b.Body, "evil-prod") {
		t.Fatalf("bump %+v", b)
	}
}

// A version waiting in staging is promoted to prod as a pull request in the repository prod's Argo CD
// application deploys from.
func TestPromoteAsPR(t *testing.T) {
	e := newUIEnv(t)
	ctx := context.Background()
	sc := e.ws.Scope()
	pulls := &fakePulls{}
	e.ui.pulls = pulls
	svc := e.serviceOf("evil")
	rec, _ := e.st.GetServiceByName(ctx, sc, svc)
	staging, _ := e.st.CreateEnvironment(ctx, sc, "staging", 20)
	stg, _ := e.st.CreateTarget(ctx, store.Target{Scope: sc, EnvironmentID: staging.ID, Platform: "kubernetes", Name: "k8s-staging"})
	if err := e.st.ApplySnapshot(ctx, store.SnapshotChanges{
		Scope: sc, SnapshotID: store.NewID(), TargetID: stg.ID, At: time.Now().Add(-48 * time.Hour),
		Upsert: []store.Instance{{
			TargetID: stg.ID, EnvironmentID: staging.ID, ServiceID: rec.ID, WorkloadID: "s1", WorkloadKind: "deployment",
			WorkloadName: "evil", ContainerName: "app", Image: "nginx:1.28.0", Tag: "1.28.0", Running: 1, IsMain: true,
		}},
	}); err != nil {
		t.Fatal(err)
	}
	g := store.WorkloadGitOps{
		WorkloadID: "w1", Tool: "argocd", App: "evil-prod", Repo: "https://github.com/acme/deploy.git",
		Path: "apps/evil/prod", TargetRevision: "main", Sync: "Synced", Health: "Healthy",
	}
	if err := e.st.SetWorkloadGitOps(ctx, sc, e.tgt.ID, []store.WorkloadGitOps{g}, true, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := e.ui.checker.EvaluateDrift(ctx, sc); err != nil { // promotions come from "behind previous env" drift
		t.Fatal(err)
	}
	member := e.as(store.RoleMember)
	if _, page := get(t, member, e.srv.URL+"/delivery", nil); !strings.Contains(page, "Open PR → prod") {
		t.Fatal("no promote button on the Delivery page")
	}
	_, body, _ := post(t, member, e.srv.URL+"/delivery/promote", url.Values{
		"service": {svc}, "env": {"prod"}, "version": {"1.28.0"},
		"target_id": {e.tgt.ID}, "workload_id": {"w1"},
	})
	if !strings.Contains(body, "Pull request opened") || len(pulls.got) != 1 {
		t.Fatalf("not opened (%d)", len(pulls.got))
	}
	if b := pulls.got[0]; b.To != "1.28.0" || b.Path != "apps/evil/prod" || !strings.Contains(b.Body, "it has run in staging since") {
		t.Fatalf("bump %+v", b)
	}
	// A promotion that does not wait is refused.
	_, body, _ = post(t, member, e.srv.URL+"/delivery/promote", url.Values{
		"service": {svc}, "env": {"prod"}, "version": {"1000.0.0"},
		"target_id": {e.tgt.ID}, "workload_id": {"w1"},
	})
	if !strings.Contains(body, "no longer waiting") || len(pulls.got) != 1 {
		t.Fatal("a promotion not waiting was opened")
	}
}

func TestVulnsMarkdown(t *testing.T) {
	if vulnsMarkdown(versions.VulnDelta{}) != "" || vulnsMarkdown(versions.VulnDelta{Known: true}) != "" {
		t.Fatal("nothing known, yet a section")
	}
	md := vulnsMarkdown(versions.VulnDelta{
		Known: true,
		Fixed: []versions.VulnRef{{ID: "CVE-2021-44228", Severity: "CRITICAL", Exploited: true}, {ID: "CVE-2024-1", Severity: "HIGH"}},
		New:   []versions.VulnRef{{ID: "GHSA-xxxx", Severity: "MODERATE"}},
	})
	for _, want := range []string{
		"Fixes 2, 1 of them exploited in the wild: `CVE-2021-44228` (**exploited in the wild**), `CVE-2024-1` (high)",
		"Brings 1 new: `GHSA-xxxx` (moderate)",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("missing %q in\n%s", want, md)
		}
	}
}

// The version policy opens pull requests by itself up to the jump it names, once per version, and the page then
// links the pull request instead of offering one.
func TestAutoPRs(t *testing.T) {
	e := newUIEnv(t)
	ctx := context.Background()
	sc := e.ws.Scope()
	pulls := &fakePulls{}
	e.ui.pulls = pulls
	g := store.WorkloadGitOps{
		WorkloadID: "w1", Tool: "argocd", App: "evil-prod", Repo: "https://github.com/acme/deploy.git",
		Path: "apps/evil/prod", TargetRevision: "main", Sync: "Synced", Health: "Healthy",
	}
	if err := e.st.SetWorkloadGitOps(ctx, sc, e.tgt.ID, []store.WorkloadGitOps{g}, true, time.Now()); err != nil {
		t.Fatal(err)
	}
	svcName := e.serviceOf("evil")
	svc, _ := e.st.GetServiceByName(ctx, sc, svcName)
	if _, err := e.st.InsertReleases(ctx, sc, svc.ID, []string{"1.28.0"}); err != nil { // the fixture runs nginx 1.27.2
		t.Fatal(err)
	}
	setPolicy := func(js string) {
		svc.VersionPolicy = json.RawMessage(js)
		if err := e.st.UpdateService(ctx, svc); err != nil {
			t.Fatal(err)
		}
	}
	run := func() int {
		n, err := e.ui.AutoPRs(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	if run() != 0 {
		t.Fatal("opened without auto_pr")
	}
	setPolicy(`{"auto_pr":{"prod":"patch"}}`)
	if run() != 0 {
		t.Fatal("opened a minor upgrade under auto_pr patch")
	}
	setPolicy(`{"auto_pr":{"prod":"minor"}}`)
	if run() != 1 || run() != 0 || len(pulls.got) != 1 {
		t.Fatalf("want one pull request, got %d", len(pulls.got))
	}
	if b := pulls.got[0]; b.To != "1.28.0" || !strings.Contains(b.Body, "auto_pr: minor") || !strings.Contains(b.Body, "Opened by auto-pr") {
		t.Fatalf("bump %+v", b)
	}
	_, page := get(t, e.as(store.RoleMember), e.srv.URL+"/services/"+url.PathEscape(svcName), nil)
	if !strings.Contains(page, `href="https://github.com/acme/deploy/pull/7"`) || strings.Contains(page, "Open PR") {
		t.Fatal("the page offers a pull request instead of linking the open one")
	}
	if got, err := parseAutoPR(" dev=minor, staging=patch "); err != nil || got["dev"] != "minor" || formatAutoPR(got) != "dev=minor, staging=patch" {
		t.Fatalf("parse %v %v", got, err)
	}
	if _, err := parseAutoPR("prod=everything"); err == nil {
		t.Fatal("a bad jump accepted")
	}
}

// The preview shows the change and the text before anything is opened; in the demo anyone may look, no one opens.
func TestPreviewGitOpsPR(t *testing.T) {
	e := newUIEnv(t)
	ctx := context.Background()
	sc := e.ws.Scope()
	pulls := &fakePulls{}
	e.ui.pulls = pulls
	g := store.WorkloadGitOps{
		WorkloadID: "w1", Tool: "argocd", App: "evil-prod", Repo: "https://github.com/acme/deploy.git",
		Path: "apps/evil/prod", TargetRevision: "main", Sync: "Synced", Health: "Healthy",
	}
	if err := e.st.SetWorkloadGitOps(ctx, sc, e.tgt.ID, []store.WorkloadGitOps{g}, true, time.Now()); err != nil {
		t.Fatal(err)
	}
	svc := e.serviceOf("evil")
	rec, _ := e.st.GetServiceByName(ctx, sc, svc)
	if _, err := e.st.InsertReleases(ctx, sc, rec.ID, []string{"1.28.0"}); err != nil {
		t.Fatal(err)
	}
	preview := e.srv.URL + "/services/" + url.PathEscape(svc) + "/gitops/preview?" + previewQuery(e.tgt.ID, "w1", "1.28.0", "")
	member, viewer := e.as(store.RoleMember), e.as(store.RoleViewer)
	_, page := get(t, member, preview, nil)
	for _, want := range []string{
		"Pull request preview", "apps/evil/prod/kustomization.yaml", `diff-del">- `, "newTag: &#34;1.27.2&#34;",
		"newTag: &#34;1.28.0&#34;", "goliash/", "Upgrades <strong>", "Open the pull request",
	} {
		if !strings.Contains(page, want) {
			t.Errorf("preview misses %q", want)
		}
	}
	if len(pulls.got) != 0 {
		t.Fatal("the preview opened a pull request")
	}
	if code, _ := get(t, viewer, preview, nil); code != http.StatusNotFound {
		t.Fatalf("a viewer previews: %d", code)
	}

	// The demo: a viewer sees the preview, without a button to open it.
	e.ui.pulls = gitops.Demo{}
	_, page = get(t, viewer, preview, nil)
	if !strings.Contains(page, "This is the demo") || strings.Contains(page, "Open the pull request") || !strings.Contains(page, "kustomization.yaml") {
		t.Fatal("the demo preview")
	}
}

func TestDiffLines(t *testing.T) {
	old := "a\nb\nc\nd\ne\nf\ng\nh\ni\n"
	got := diffLines(old, strings.Replace(old, "h", "H", 1), 1)
	var b strings.Builder
	for _, l := range got {
		b.WriteString(diffMark(l.Kind) + l.Text + "|")
	}
	if b.String() != "…|  g|- h|+ H|  i|" {
		t.Fatalf("diff %q", b.String())
	}
}

func TestMarkdownHTML(t *testing.T) {
	got := markdownHTML("Upgrades **a** to `1.2`: <script>.\n\n- one [x](https://e.com/p)\n- two\n\n### Known\n\ntext")
	want := `<p>Upgrades <strong>a</strong> to <code>1.2</code>: &lt;script&gt;.</p><ul><li>one <a href="https://e.com/p" target="_blank" rel="noopener">x</a></li><li>two</li></ul><h3>Known</h3><p>text</p>`
	if got != want {
		t.Fatalf("markdown\n got: %s\nwant: %s", got, want)
	}
}

// Goli's summary of the release notes goes into the description, after the facts, marked as Goli's.
func TestBumpRequestWithSummary(t *testing.T) {
	s := &Server{publicURL: "https://goliash.example.com"}
	u := upgrade{
		app: versions.GitOpsApp{Tag: "1.4.2", Image: "ghcr.io/acme/api:1.4.2"}, service: "api", page: "/services/api",
		to: "1.5.0", why: "the newest release its version policy accepts", summary: "- Nothing breaking in the release notes.\n- Fixes CVE-2026-1.",
	}
	u.app.Environment, u.app.Path, u.app.Repo = "prod", "apps/api", "https://github.com/acme/deploy"
	b := s.bumpRequest(u, "you@example.com")
	if !strings.Contains(b.Body, "### What changes\n\n- Nothing breaking in the release notes.\n- Fixes CVE-2026-1.") ||
		!strings.Contains(b.Body, "by Goli") {
		t.Fatalf("body:\n%s", b.Body)
	}
	if b2 := s.bumpRequest(upgrade{app: u.app, service: "api", to: "1.5.0"}, "x"); strings.Contains(b2.Body, "What changes") {
		t.Fatal("a summary section without a summary")
	}
}

// A pull request closed without merging: the page offers the upgrade again, the policy does not open it again.
func TestClosedPullRequest(t *testing.T) {
	e := newUIEnv(t)
	ctx := context.Background()
	sc := e.ws.Scope()
	pulls := &fakePulls{state: "open"}
	e.ui.pulls = pulls
	g := store.WorkloadGitOps{
		WorkloadID: "w1", Tool: "argocd", App: "evil-prod", Repo: "https://github.com/acme/deploy.git",
		Path: "apps/evil/prod", TargetRevision: "main", Sync: "Synced", Health: "Healthy",
	}
	if err := e.st.SetWorkloadGitOps(ctx, sc, e.tgt.ID, []store.WorkloadGitOps{g}, true, time.Now()); err != nil {
		t.Fatal(err)
	}
	svcName := e.serviceOf("evil")
	svc, _ := e.st.GetServiceByName(ctx, sc, svcName)
	if _, err := e.st.InsertReleases(ctx, sc, svc.ID, []string{"1.28.0"}); err != nil {
		t.Fatal(err)
	}
	svc.VersionPolicy = json.RawMessage(`{"auto_pr":{"prod":"minor"}}`)
	if err := e.st.UpdateService(ctx, svc); err != nil {
		t.Fatal(err)
	}
	if n, _ := e.ui.AutoPRs(ctx); n != 1 {
		t.Fatalf("opened %d", n)
	}
	m := e.as(store.RoleMember)
	page := func() string {
		_, b := get(t, m, e.srv.URL+"/services/"+url.PathEscape(svcName), nil)
		return b
	}
	member := page()
	if !strings.Contains(member, "Pull request → 1.28.0") {
		t.Fatal("no link to the open pull request")
	}
	if _, d := get(t, m, e.srv.URL+"/delivery", nil); !strings.Contains(d, "<h2>Pull requests</h2>") || !strings.Contains(d, "1.27.2 → 1.28.0") ||
		!strings.Contains(d, "by auto-pr") || !strings.Contains(d, `class="badge upstream">open`) {
		t.Fatal("the Delivery page does not list the pull request")
	}
	// Its checks fail: the service page and the Delivery page say so next to the link.
	pulls.checks = "failing"
	if err := e.ui.RefreshPulls(ctx); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(page(), "checks fail") {
		t.Fatal("failing checks not shown on the service page")
	}
	if _, d := get(t, m, e.srv.URL+"/delivery", nil); !strings.Contains(d, "checks fail") {
		t.Fatal("failing checks not shown on the Delivery page")
	}
	pulls.state = "merged"
	if err := e.ui.RefreshPulls(ctx); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(page(), "Pull request → 1.28.0 · merged") {
		t.Fatal("not shown as merged")
	}
	// Merged while 1.27.2 still runs: the Delivery page says it has not deployed.
	if _, d := get(t, m, e.srv.URL+"/delivery", nil); !strings.Contains(d, ">not deployed</span>") || !strings.Contains(d, "merged ") {
		t.Fatal("a merged pull request not deployed yet is not shown as such")
	}
	pulls.state = "closed"
	got, _ := e.st.ListGitOpsPulls(ctx, sc)
	_ = e.st.SetGitOpsPullState(ctx, sc, got[0].ID, "closed", time.Time{})
	if p := page(); !strings.Contains(p, "Open PR → 1.28.0") {
		t.Fatal("a closed pull request is not offered again")
	}
	if n, _ := e.ui.AutoPRs(ctx); n != 0 {
		t.Fatal("the policy opened a closed pull request again")
	}
}

// A degraded application whose version replaced another this week is offered a pull request back to it, which
// says it rolls back and why.
func TestRollbackPR(t *testing.T) {
	e := newUIEnv(t)
	ctx := context.Background()
	sc := e.ws.Scope()
	pulls := &fakePulls{}
	e.ui.pulls = pulls
	g := store.WorkloadGitOps{
		WorkloadID: "w1", Tool: "argocd", App: "evil-prod", Repo: "https://github.com/acme/deploy.git",
		Path: "apps/evil/prod", TargetRevision: "main", Sync: "Synced", Health: "Healthy",
	}
	if err := e.st.SetWorkloadGitOps(ctx, sc, e.tgt.ID, []store.WorkloadGitOps{g}, true, time.Now().Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	svc := e.serviceOf("evil")
	svcRec, _ := e.st.GetServiceByName(ctx, sc, svc)
	o, _ := versions.LoadOverview(ctx, e.st, sc)
	apps, _ := versions.GitOpsApps(ctx, e.st, sc, o)
	if len(apps) != 1 {
		t.Fatalf("apps %+v", apps)
	}
	running := apps[0].Tag
	if err := e.st.InsertEvent(ctx, sc, store.Event{
		Type: "version_changed", ServiceID: svcRec.ID, TargetID: e.tgt.ID, FromVersion: "0.9.0", ToVersion: running,
		At: time.Now().Add(-2 * time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	page := "/services/" + url.PathEscape(svc)
	member := e.as(store.RoleMember)
	if _, body := get(t, member, e.srv.URL+page, nil); strings.Contains(body, "Rollback PR") {
		t.Fatal("a healthy application is offered a rollback")
	}

	g.Health = "Degraded"
	if err := e.st.SetWorkloadGitOps(ctx, sc, e.tgt.ID, []store.WorkloadGitOps{g}, true, time.Now().Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, body := get(t, member, e.srv.URL+page, nil); !strings.Contains(body, "Rollback PR → 0.9.0") {
		t.Fatal("no Rollback PR button")
	}
	if _, body := get(t, e.as(store.RoleViewer), e.srv.URL+page, nil); strings.Contains(body, "Rollback PR") {
		t.Fatal("a viewer is offered a rollback")
	}
	_, body, _ := post(t, member, e.srv.URL+page+"/gitops/pr",
		url.Values{"target_id": {e.tgt.ID}, "workload_id": {"w1"}, "to": {"0.9.0"}})
	if !strings.Contains(body, "Pull request opened") || len(pulls.got) != 1 {
		t.Fatalf("not opened: %d", len(pulls.got))
	}
	b := pulls.got[0]
	if b.Title != "Roll back "+svc+" to 0.9.0 in "+apps[0].Environment || !strings.HasPrefix(b.Head, "goliash/rollback-") ||
		!strings.Contains(b.Body, "Rolls back") || !strings.Contains(b.Body, "has been degraded since") {
		t.Fatalf("pull request %+v", b)
	}
	if _, body := get(t, member, e.srv.URL+page, nil); !strings.Contains(body, "https://github.com/acme/deploy/pull/7") {
		t.Fatal("the opened rollback is not linked")
	}
	// Only the version it replaced: no other.
	_, body, _ = post(t, member, e.srv.URL+page+"/gitops/pr",
		url.Values{"target_id": {e.tgt.ID}, "workload_id": {"w1"}, "to": {"0.1.0"}})
	if !strings.Contains(body, "no longer offered") {
		t.Fatal("a rollback to another version was opened")
	}
}

// The Delivery page says how long after opening a pull request its version ran, and the median.
func TestPullToRunning(t *testing.T) {
	e := newUIEnv(t)
	ctx := context.Background()
	sc := e.ws.Scope()
	e.ui.pulls = &fakePulls{}
	g := store.WorkloadGitOps{
		WorkloadID: "w1", Tool: "argocd", App: "evil-prod", Repo: "https://github.com/acme/deploy.git",
		Path: "apps/evil/prod", TargetRevision: "main", Sync: "Synced", Health: "Healthy",
	}
	if err := e.st.SetWorkloadGitOps(ctx, sc, e.tgt.ID, []store.WorkloadGitOps{g}, true, time.Now()); err != nil {
		t.Fatal(err)
	}
	svc, _ := e.st.GetServiceByName(ctx, sc, e.serviceOf("evil"))
	o, _ := versions.LoadOverview(ctx, e.st, sc)
	apps, _ := versions.GitOpsApps(ctx, e.st, sc, o)
	running := apps[0].Tag
	if err := e.st.RecordGitOpsPull(ctx, store.GitOpsPull{
		Scope: sc, ServiceID: svc.ID, Environment: apps[0].Environment, TargetID: e.tgt.ID, WorkloadID: "w1",
		From: "0.9.0", To: running, URL: "https://github.com/acme/deploy/pull/9", OpenedBy: "auto-pr",
	}); err != nil {
		t.Fatal(err)
	}
	pulls, _ := e.st.ListGitOpsPulls(ctx, sc) // merged two hours after opening, as the forge says
	if err := e.st.SetGitOpsPullState(ctx, sc, pulls[0].ID, "merged", time.Now().Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := e.st.InsertEvent(ctx, sc, store.Event{
		Type: "version_changed", ServiceID: svc.ID, TargetID: e.tgt.ID, FromVersion: "0.9.0", ToVersion: running,
		At: time.Now().Add(3*time.Hour + time.Minute),
	}); err != nil {
		t.Fatal(err)
	}
	_, d := get(t, e.as(store.RoleViewer), e.srv.URL+"/delivery", nil)
	if !strings.Contains(d, "3h after opening") || !strings.Contains(d, "3h from pull request to running") {
		t.Fatal("no time from pull request to running")
	}
	if !strings.Contains(d, "1h after merging") || !strings.Contains(d, "1h from merge to running") {
		t.Fatal("no time from merge to running")
	}
}

// An application out of sync says which version Git pins, read in the background, when it is not the one running.
func TestGitPins(t *testing.T) {
	e := newUIEnv(t)
	ctx := context.Background()
	sc := e.ws.Scope()
	pulls := &fakePulls{pinned: []string{"9.9.9"}}
	e.ui.pulls = pulls
	g := store.WorkloadGitOps{
		WorkloadID: "w1", Tool: "argocd", App: "evil-prod", Repo: "https://github.com/acme/pins.git",
		Path: "apps/evil/prod", TargetRevision: "main", Sync: "OutOfSync", Health: "Healthy",
	}
	if err := e.st.SetWorkloadGitOps(ctx, sc, e.tgt.ID, []store.WorkloadGitOps{g}, true, time.Now().Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	page := e.srv.URL + "/services/" + url.PathEscape(e.serviceOf("evil"))
	viewer := e.as(store.RoleViewer)
	deadline := time.Now().Add(5 * time.Second)
	for {
		_, body := get(t, viewer, page, nil)
		if strings.Contains(body, `Git pins <span class="mono">9.9.9</span>`) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("no Git pins")
		}
		time.Sleep(50 * time.Millisecond)
	}
	if _, matrix := get(t, viewer, e.srv.URL+"/?view=table", nil); !strings.Contains(matrix, "Git pins 9.9.9 (") {
		t.Fatal("the matrix does not say what Git pins")
	}
	o, _ := versions.LoadOverview(ctx, e.st, sc)
	apps, _ := versions.GitOpsApps(ctx, e.st, sc, o)
	if got := e.ui.GitPinsNow(ctx, apps[0]); got != "9.9.9" {
		t.Fatalf("GitPinsNow = %q", got)
	}
	// Git pins the newest release already: no pull request to offer, it waits for a sync.
	svc, _ := e.st.GetServiceByName(ctx, sc, e.serviceOf("evil"))
	if _, err := e.st.InsertReleases(ctx, sc, svc.ID, []string{"9.9.9"}); err != nil {
		t.Fatal(err)
	}
	if _, body := get(t, e.as(store.RoleMember), page, nil); strings.Contains(body, "Open PR → 9.9.9") {
		t.Fatal("a pull request offered for what Git pins already")
	}
}

// The monthly report counts the pull requests Goliash opened, rollbacks among them, and how soon their version ran.
func TestReportPulls(t *testing.T) {
	e := newUIEnv(t)
	ctx := context.Background()
	sc := e.ws.Scope()
	svc, _ := e.st.GetServiceByName(ctx, sc, e.serviceOf("evil"))
	for _, p := range []store.GitOpsPull{
		{From: "1.0.0", To: "1.1.0", OpenedBy: "auto-pr", URL: "https://github.com/acme/deploy/pull/1", WorkloadID: "w1"},
		{From: "1.1.0", To: "1.0.0", OpenedBy: "sre@example.com", URL: "https://github.com/acme/deploy/pull/2", WorkloadID: "w2"},
	} {
		p.Scope, p.ServiceID, p.Environment, p.TargetID = sc, svc.ID, "prod", e.tgt.ID
		if err := e.st.RecordGitOpsPull(ctx, p); err != nil {
			t.Fatal(err)
		}
	}
	if err := e.st.InsertEvent(ctx, sc, store.Event{
		Type: "version_changed", ServiceID: svc.ID, TargetID: e.tgt.ID, FromVersion: "1.0.0", ToVersion: "1.1.0",
		At: time.Now().Add(2*time.Hour + time.Minute),
	}); err != nil {
		t.Fatal(err)
	}
	month := time.Now().UTC().Format("2006-01")
	_, page := get(t, e.as(store.RoleViewer), e.srv.URL+"/report?month="+month, nil)
	for _, want := range []string{
		"<h2>Pull requests</h2>", "<strong>2</strong><span>opened</span>", "<strong>1</strong><span>by version policies</span>",
		"<strong>1</strong><span>rollbacks</span>", "<strong>2h</strong><span>from pull request to running</span>",
	} {
		if !strings.Contains(page, want) {
			t.Errorf("report lacks %q", want)
		}
	}
}

// A pull request Renovate (or anyone) opened for the same upgrade is linked, not duplicated: by the page, the form
// and the version policy.
func TestOthersPull(t *testing.T) {
	e := newUIEnv(t)
	ctx := context.Background()
	sc := e.ws.Scope()
	renovate := gitops.Pull{URL: "https://github.com/acme/others/pull/41", Title: "Update docker.io/library/nginx Docker tag to v1.28.0", Branch: "renovate/nginx-1.x"}
	pulls := &fakePulls{others: []gitops.Pull{renovate}}
	e.ui.pulls = pulls
	g := store.WorkloadGitOps{
		WorkloadID: "w1", Tool: "argocd", App: "evil-prod", Repo: "https://github.com/acme/others.git",
		Path: "apps/evil/prod", TargetRevision: "main", Sync: "Synced", Health: "Healthy",
	}
	if err := e.st.SetWorkloadGitOps(ctx, sc, e.tgt.ID, []store.WorkloadGitOps{g}, true, time.Now()); err != nil {
		t.Fatal(err)
	}
	svcName := e.serviceOf("evil")
	svc, _ := e.st.GetServiceByName(ctx, sc, svcName)
	if _, err := e.st.InsertReleases(ctx, sc, svc.ID, []string{"1.28.0"}); err != nil { // the fixture runs nginx 1.27.2
		t.Fatal(err)
	}
	page := e.srv.URL + "/services/" + url.PathEscape(svcName)
	member := e.as(store.RoleMember)
	deadline := time.Now().Add(5 * time.Second)
	for {
		_, body := get(t, member, page, nil)
		if strings.Contains(body, renovate.URL) && strings.Contains(body, "Opened outside Goliash: "+renovate.Title) && !strings.Contains(body, "Open PR → 1.28.0") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("Renovate's pull request is not linked")
		}
		time.Sleep(50 * time.Millisecond)
	}
	_, body, _ := post(t, member, page+"/gitops/pr", url.Values{"target_id": {e.tgt.ID}, "workload_id": {"w1"}, "to": {"1.28.0"}})
	if !strings.Contains(body, "Someone opened this upgrade already: "+renovate.URL) || len(pulls.got) != 0 {
		t.Fatalf("a duplicate was opened: %d", len(pulls.got))
	}
	svc.VersionPolicy = json.RawMessage(`{"auto_pr":{"prod":"minor"}}`)
	if err := e.st.UpdateService(ctx, svc); err != nil {
		t.Fatal(err)
	}
	if n, _ := e.ui.AutoPRs(ctx); n != 0 || len(pulls.got) != 0 {
		t.Fatal("the version policy duplicated Renovate's pull request")
	}
}

// The GitOps page lists every application, those that need attention first, and filters them.
func TestGitOpsPage(t *testing.T) {
	e := newUIEnv(t)
	ctx := context.Background()
	sc := e.ws.Scope()
	viewer := e.as(store.RoleViewer)
	if _, body := get(t, viewer, e.srv.URL+"/gitops", nil); !strings.Contains(body, "No Argo CD application") {
		t.Fatal("no empty state")
	}
	apps := []store.WorkloadGitOps{
		{WorkloadID: "w1", Tool: "argocd", App: "evil-prod", Repo: "https://github.com/acme/deploy.git", Path: "apps/evil/prod", Sync: "Synced", Health: "Healthy"},
	}
	if err := e.st.SetWorkloadGitOps(ctx, sc, e.tgt.ID, apps, true, time.Now().Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	_, body := get(t, viewer, e.srv.URL+"/gitops", nil)
	if !strings.Contains(body, "evil-prod") || !strings.Contains(body, "<strong>1</strong><span>deployed workloads</span>") {
		t.Fatal("the application is not listed")
	}
	if _, body := get(t, viewer, e.srv.URL+"/gitops?only=attention", nil); strings.Contains(body, "evil-prod") || !strings.Contains(body, "in sync and healthy") {
		t.Fatal("a healthy application shown as needing attention")
	}
	apps[0].Health = "Degraded"
	if err := e.st.SetWorkloadGitOps(ctx, sc, e.tgt.ID, apps, true, time.Now().Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, body := get(t, viewer, e.srv.URL+"/gitops?only=attention", nil); !strings.Contains(body, "evil-prod") || !strings.Contains(body, "<strong>1</strong><span>degraded or missing</span>") {
		t.Fatal("a degraded application not shown")
	}
}

// A degraded application is not offered a rollback of a change days before its trouble began: that change ran
// healthy since.
func TestRollbackOldChange(t *testing.T) {
	e := newUIEnv(t)
	ctx := context.Background()
	sc := e.ws.Scope()
	e.ui.pulls = &fakePulls{}
	g := store.WorkloadGitOps{
		WorkloadID: "w1", Tool: "argocd", App: "evil-prod", Repo: "https://github.com/acme/deploy.git",
		Path: "apps/evil/prod", TargetRevision: "main", Sync: "Synced", Health: "Degraded",
	}
	if err := e.st.SetWorkloadGitOps(ctx, sc, e.tgt.ID, []store.WorkloadGitOps{g}, true, time.Now().Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	svc, _ := e.st.GetServiceByName(ctx, sc, e.serviceOf("evil"))
	o, _ := versions.LoadOverview(ctx, e.st, sc)
	apps, _ := versions.GitOpsApps(ctx, e.st, sc, o)
	if err := e.st.InsertEvent(ctx, sc, store.Event{
		Type: "version_changed", ServiceID: svc.ID, TargetID: e.tgt.ID, FromVersion: "0.9.0", ToVersion: apps[0].Tag,
		At: time.Now().Add(-3 * 24 * time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	if to, _ := e.ui.rollbackFor(ctx, sc, apps[0], time.Now()); to != "" {
		t.Fatalf("a change three days before the trouble offered as its cause: %s", to)
	}
}

// A service without a GitOps panel is told how to name its place in Git on the platforms that take labels; on
// Kubernetes (labels cannot hold an address) Argo CD or Flux tells, so it is not.
func TestGitLabelTip(t *testing.T) {
	if sn := gitLabelSnippets("vault", []string{"kubernetes", "nomad", "ecs"}); len(sn) != 2 ||
		!strings.Contains(sn[0].Code, `"goliash.git.path" = "nomad/vault.nomad.hcl"`) || !strings.Contains(sn[1].Where, "ECS") {
		t.Fatalf("snippets %+v", sn)
	}
	e := newUIEnv(t)
	e.ui.pulls = &fakePulls{}
	if _, body := get(t, e.as(store.RoleMember), e.srv.URL+"/services/"+url.PathEscape(e.serviceOf("evil")), nil); strings.Contains(body, "Open upgrades as pull requests") {
		t.Fatal("a label tip for a service on Kubernetes")
	}
}

// The Updates page opens the ticked upgrades as pull requests in one go, and says what it could not open.
func TestBulkPRs(t *testing.T) {
	e := newUIEnv(t)
	ctx := context.Background()
	sc := e.ws.Scope()
	pulls := &fakePulls{}
	e.ui.pulls = pulls
	g := store.WorkloadGitOps{
		WorkloadID: "w1", Tool: "argocd", App: "evil-prod", Repo: "https://github.com/acme/deploy.git",
		Path: "apps/evil/prod", TargetRevision: "main", Sync: "Synced", Health: "Healthy",
	}
	if err := e.st.SetWorkloadGitOps(ctx, sc, e.tgt.ID, []store.WorkloadGitOps{g}, true, time.Now()); err != nil {
		t.Fatal(err)
	}
	svc := e.serviceOf("evil")
	rec, _ := e.st.GetServiceByName(ctx, sc, svc)
	if _, err := e.st.InsertReleases(ctx, sc, rec.ID, []string{"1.28.0"}); err != nil {
		t.Fatal(err)
	}
	_ = e.ui.checker.EvaluateDrift(ctx, sc)
	member := e.as(store.RoleMember)
	want := svc + "|" + e.tgt.ID + "|w1|1.28.0"
	if _, page := get(t, member, e.srv.URL+"/updates", nil); !strings.Contains(page, `form="bulk-prs"`) || !strings.Contains(page, "Open the ticked pull requests") {
		t.Fatal("no ticks on the Updates page")
	}
	if _, page := get(t, e.as(store.RoleViewer), e.srv.URL+"/updates", nil); strings.Contains(page, "bulk-prs") {
		t.Fatal("a viewer may tick pull requests")
	}
	_, body, _ := post(t, member, e.srv.URL+"/updates/prs", url.Values{"pr": {want, "nope|x|y|9.9.9"}, "return": {"/updates"}})
	if len(pulls.got) != 1 || !strings.Contains(body, "Opened the pull request for "+html.EscapeString(svc)+" 1.28.0") || !strings.Contains(body, "Not opened: nope 9.9.9 (no longer offered)") {
		t.Fatalf("opened %d: %.300s", len(pulls.got), body)
	}
}

// Two services of one Argo CD application waiting for prod go as one pull request: its preview, and opening it
// records it for both.
func TestPromoteApplication(t *testing.T) {
	e := newUIEnv(t)
	ctx := context.Background()
	sc := e.ws.Scope()
	pulls := &fakePulls{}
	e.ui.pulls = pulls
	evil := e.serviceOf("evil")
	rec, _ := e.st.GetServiceByName(ctx, sc, evil)
	cache, err := e.st.EnsureService(ctx, sc, "cache")
	if err != nil {
		t.Fatal(err)
	}
	staging, _ := e.st.CreateEnvironment(ctx, sc, "staging", 20)
	stg, _ := e.st.CreateTarget(ctx, store.Target{Scope: sc, EnvironmentID: staging.ID, Platform: "kubernetes", Name: "k8s-staging"})
	inst := func(tgt, env, svc, wl, image, tag string) store.Instance {
		return store.Instance{
			TargetID: tgt, EnvironmentID: env, ServiceID: svc, WorkloadID: wl, WorkloadKind: "deployment",
			WorkloadName: wl, ContainerName: "app", Image: image + ":" + tag, Tag: tag, Running: 1, IsMain: true,
		}
	}
	if err := e.st.ApplySnapshot(ctx, store.SnapshotChanges{
		Scope: sc, SnapshotID: store.NewID(), TargetID: stg.ID, At: time.Now().Add(-48 * time.Hour),
		Upsert: []store.Instance{inst(stg.ID, staging.ID, rec.ID, "s1", "nginx", "1.28.0"), inst(stg.ID, staging.ID, cache.ID, "s2", "redis", "7.2.5")},
	}); err != nil {
		t.Fatal(err)
	}
	if err := e.st.ApplySnapshot(ctx, store.SnapshotChanges{
		Scope: sc, SnapshotID: store.NewID(), TargetID: e.tgt.ID, At: time.Now().Add(-72 * time.Hour),
		Upsert: []store.Instance{inst(e.tgt.ID, e.prod.ID, cache.ID, "w2", "redis", "7.2.4")},
	}); err != nil {
		t.Fatal(err)
	}
	apps := []store.WorkloadGitOps{
		{WorkloadID: "w1", Tool: "argocd", App: "shop-prod", Repo: "https://github.com/acme/deploy.git", Path: "apps/shop/prod", TargetRevision: "main", Sync: "Synced", Health: "Healthy"},
		{WorkloadID: "w2", Tool: "argocd", App: "shop-prod", Repo: "https://github.com/acme/deploy.git", Path: "apps/shop/prod", TargetRevision: "main", Sync: "Synced", Health: "Healthy"},
	}
	if err := e.st.SetWorkloadGitOps(ctx, sc, e.tgt.ID, apps, true, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := e.ui.checker.EvaluateDrift(ctx, sc); err != nil {
		t.Fatal(err)
	}
	member := e.as(store.RoleMember)
	_, page := get(t, member, e.srv.URL+"/delivery", nil)
	if !strings.Contains(page, "Promote shop-prod → prod") {
		t.Fatal("no application promotion on the Delivery page")
	}
	q := url.Values{"app": {"shop-prod"}, "target_id": {e.tgt.ID}, "from": {"staging"}, "to": {"prod"}}
	if _, prev := get(t, member, e.srv.URL+"/delivery/promote-app/preview?"+q.Encode(), nil); !strings.Contains(prev, "Promote shop-prod to prod: ") || !strings.Contains(prev, "cache 7.2.5") {
		t.Fatal("no preview of the application promotion")
	}
	_, body, _ := post(t, member, e.srv.URL+"/delivery/promote-app", q)
	if len(pulls.got) != 1 || !strings.Contains(body, "Pull request opened for shop-prod") {
		t.Fatalf("not opened (%d)", len(pulls.got))
	}
	b := pulls.got[0]
	if len(b.Also) != 1 || b.Head != "goliash/promote-shop-prod-prod" || !strings.Contains(b.Body, "Promotes **2 services**") {
		t.Fatalf("bump %+v", b)
	}
	if got, _ := e.st.ListGitOpsPulls(ctx, sc); len(got) != 2 || got[0].URL != got[1].URL {
		t.Fatalf("recorded %+v", got)
	}
	if _, page = get(t, member, e.srv.URL+"/delivery", nil); strings.Contains(page, "Promote shop-prod → prod") {
		t.Fatal("offered again once opened")
	}
}

// Ticked upgrades in one repository, branch and environment go as one pull request when asked.
func TestBulkPRsGrouped(t *testing.T) {
	e := newUIEnv(t)
	ctx := context.Background()
	sc := e.ws.Scope()
	pulls := &fakePulls{}
	e.ui.pulls = pulls
	evil := e.serviceOf("evil")
	rec, _ := e.st.GetServiceByName(ctx, sc, evil)
	cache, _ := e.st.EnsureService(ctx, sc, "cache")
	if err := e.st.ApplySnapshot(ctx, store.SnapshotChanges{
		Scope: sc, SnapshotID: store.NewID(), TargetID: e.tgt.ID, At: time.Now(),
		Upsert: []store.Instance{{
			TargetID: e.tgt.ID, EnvironmentID: e.prod.ID, ServiceID: cache.ID, WorkloadID: "w2", WorkloadKind: "deployment",
			WorkloadName: "cache", ContainerName: "app", Image: "redis:7.2.4", Tag: "7.2.4", Running: 1, IsMain: true,
		}},
	}); err != nil {
		t.Fatal(err)
	}
	apps := []store.WorkloadGitOps{
		{WorkloadID: "w1", Tool: "argocd", App: "a", Repo: "https://github.com/acme/deploy.git", Path: "apps/a", TargetRevision: "main", Sync: "Synced", Health: "Healthy"},
		{WorkloadID: "w2", Tool: "argocd", App: "b", Repo: "https://github.com/acme/deploy.git", Path: "apps/b", TargetRevision: "main", Sync: "Synced", Health: "Healthy"},
	}
	if err := e.st.SetWorkloadGitOps(ctx, sc, e.tgt.ID, apps, true, time.Now()); err != nil {
		t.Fatal(err)
	}
	_, _ = e.st.InsertReleases(ctx, sc, rec.ID, []string{"1.28.0"})
	_, _ = e.st.InsertReleases(ctx, sc, cache.ID, []string{"7.2.5"})
	rec.VersionPolicy = json.RawMessage(`{"pr_reviewers":["alice"],"pr_labels":["deps"]}`)
	_ = e.st.UpdateService(ctx, rec)
	cache.VersionPolicy = json.RawMessage(`{"pr_reviewers":["@acme/cache","alice"]}`)
	_ = e.st.UpdateService(ctx, cache)
	_ = e.ui.checker.EvaluateDrift(ctx, sc)
	_, body, _ := post(t, e.as(store.RoleMember), e.srv.URL+"/updates/prs", url.Values{
		"pr": {evil + "|" + e.tgt.ID + "|w1|1.28.0", "cache|" + e.tgt.ID + "|w2|7.2.5"}, "group": {"repo"}, "return": {"/updates"},
	})
	if len(pulls.got) != 1 || !strings.Contains(body, "(one pull request)") {
		t.Fatalf("opened %d: %.200s", len(pulls.got), body)
	}
	if b := pulls.got[0]; len(b.Reviewers) != 2 || !slices.Contains(b.Reviewers, "@acme/cache") || strings.Join(b.Labels, ",") != "deps" {
		t.Fatalf("reviewers %v labels %v", b.Reviewers, b.Labels)
	}
	if b := pulls.got[0]; len(b.Also) != 1 || !strings.HasPrefix(b.Title, "Upgrade 2 services in prod: ") || !strings.HasPrefix(b.Head, "goliash/upgrade-prod-") {
		t.Fatalf("bump %+v", b)
	}
	if got, _ := e.st.ListGitOpsPulls(ctx, sc); len(got) != 2 {
		t.Fatalf("recorded %d", len(got))
	}
}

// The service's version policy names reviewers and labels for its pull requests: saved from the form, sent along.
func TestPRReviewersFromPolicy(t *testing.T) {
	e := newUIEnv(t)
	ctx := context.Background()
	sc := e.ws.Scope()
	pulls := &fakePulls{}
	e.ui.pulls = pulls
	g := store.WorkloadGitOps{
		WorkloadID: "w1", Tool: "argocd", App: "evil-prod", Repo: "https://github.com/acme/deploy.git",
		Path: "apps/evil/prod", TargetRevision: "main", Sync: "Synced", Health: "Healthy",
	}
	if err := e.st.SetWorkloadGitOps(ctx, sc, e.tgt.ID, []store.WorkloadGitOps{g}, true, time.Now()); err != nil {
		t.Fatal(err)
	}
	svc := e.serviceOf("evil")
	rec, _ := e.st.GetServiceByName(ctx, sc, svc)
	rec.VersionPolicy = json.RawMessage(`{"pr_reviewers":["alice","@acme/payments"],"pr_labels":["dependencies"]}`)
	if err := e.st.UpdateService(ctx, rec); err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.InsertReleases(ctx, sc, rec.ID, []string{"1.28.0"}); err != nil {
		t.Fatal(err)
	}
	member := e.as(store.RoleMember)
	if _, page := get(t, member, e.srv.URL+"/services/"+url.PathEscape(svc), nil); !strings.Contains(page, `value="alice, @acme/payments"`) {
		t.Fatal("the reviewers are not in the form")
	}
	post(t, member, e.srv.URL+"/services/"+url.PathEscape(svc)+"/gitops/pr", url.Values{"target_id": {e.tgt.ID}, "workload_id": {"w1"}, "to": {"1.28.0"}})
	if len(pulls.got) != 1 || strings.Join(pulls.got[0].Reviewers, ",") != "alice,@acme/payments" || strings.Join(pulls.got[0].Labels, ",") != "dependencies" {
		t.Fatalf("bump %+v", pulls.got)
	}
}

func TestLicensesMarkdown(t *testing.T) {
	if licensesMarkdown(versions.LicenseDelta{}) != "" || licensesMarkdown(versions.LicenseDelta{Known: true}) != "" {
		t.Fatal("a section without changes")
	}
	md := licensesMarkdown(versions.LicenseDelta{Known: true, Changes: []versions.LicenseChange{
		{Name: "itextpdf", Version: "5.5.13", License: "AGPL-3.0-only", Class: versions.NetworkCopyleft},
		{Name: "vault", Version: "1.15.0", License: "BUSL-1.1", Class: versions.SourceAvailable, Was: "MPL-2.0"},
	}})
	for _, want := range []string{"### Licenses", "`itextpdf` 5.5.13 under **AGPL-3.0-only** (network copyleft)", "`vault` 1.15.0 under **BUSL-1.1** (source-available, was MPL-2.0)", "not legal advice"} {
		if !strings.Contains(md, want) {
			t.Errorf("missing %q in %s", want, md)
		}
	}
}

// A matrix cell offers the pull request to its newest acceptable release to members, and links one opened.
func TestMatrixCellPR(t *testing.T) {
	e := newUIEnv(t)
	ctx := context.Background()
	sc := e.ws.Scope()
	e.ui.pulls = &fakePulls{}
	g := store.WorkloadGitOps{
		WorkloadID: "w1", Tool: "argocd", App: "evil-prod", Repo: "https://github.com/acme/deploy.git",
		Path: "apps/evil/prod", TargetRevision: "main", Sync: "Synced", Health: "Healthy",
	}
	if err := e.st.SetWorkloadGitOps(ctx, sc, e.tgt.ID, []store.WorkloadGitOps{g}, true, time.Now()); err != nil {
		t.Fatal(err)
	}
	rec, _ := e.st.GetServiceByName(ctx, sc, e.serviceOf("evil"))
	if _, err := e.st.InsertReleases(ctx, sc, rec.ID, []string{"1.28.0"}); err != nil {
		t.Fatal(err)
	}
	_ = e.ui.checker.EvaluateDrift(ctx, sc)
	member := e.as(store.RoleMember)
	_, page := get(t, member, e.srv.URL+"/?view=table", nil)
	if strings.Count(page, `class="cell-pr offer noprint"`) != 1 || !strings.Contains(page, "PR → 1.28.0") {
		t.Fatal("no pull request offered in the matrix")
	}
	if _, page := get(t, e.as(store.RoleViewer), e.srv.URL+"/?view=table", nil); strings.Contains(page, "cell-pr") {
		t.Fatal("a viewer is offered a pull request in the matrix")
	}
	if err := e.st.RecordGitOpsPull(ctx, store.GitOpsPull{
		Scope: sc, ServiceID: rec.ID, Environment: "prod", TargetID: e.tgt.ID, WorkloadID: "w1",
		From: "1.27.2", To: "1.28.0", URL: "https://github.com/acme/deploy/pull/7", OpenedBy: "x",
	}); err != nil {
		t.Fatal(err)
	}
	if _, page := get(t, member, e.srv.URL+"/?view=table", nil); !strings.Contains(page, `href="https://github.com/acme/deploy/pull/7"`) || strings.Contains(page, "cell-pr offer") {
		t.Fatal("the opened pull request is not linked in the matrix")
	}
}

// A change freeze shows on every page, holds the version policy's pull requests back, and marks those opened by hand.
func TestChangeFreeze(t *testing.T) {
	e := newUIEnv(t)
	ctx := context.Background()
	sc := e.ws.Scope()
	pulls := &fakePulls{}
	e.ui.pulls = pulls
	g := store.WorkloadGitOps{
		WorkloadID: "w1", Tool: "argocd", App: "evil-prod", Repo: "https://github.com/acme/deploy.git",
		Path: "apps/evil/prod", TargetRevision: "main", Sync: "Synced", Health: "Healthy",
	}
	if err := e.st.SetWorkloadGitOps(ctx, sc, e.tgt.ID, []store.WorkloadGitOps{g}, true, time.Now()); err != nil {
		t.Fatal(err)
	}
	svc := e.serviceOf("evil")
	rec, _ := e.st.GetServiceByName(ctx, sc, svc)
	rec.VersionPolicy = json.RawMessage(`{"auto_pr":{"prod":"minor"}}`)
	_ = e.st.UpdateService(ctx, rec)
	if _, err := e.st.InsertReleases(ctx, sc, rec.ID, []string{"1.28.0"}); err != nil {
		t.Fatal(err)
	}
	admin := e.as(store.RoleAdmin)
	now := time.Now().UTC()
	post(t, admin, e.srv.URL+"/settings/freezes", url.Values{
		"from": {now.Add(-time.Hour).Format("2006-01-02T15:04")}, "to": {now.Add(24 * time.Hour).Format("2006-01-02T15:04")}, "note": {"Christmas"},
	})
	if _, page := get(t, e.as(store.RoleViewer), e.srv.URL+"/updates", nil); !strings.Contains(page, "Change freeze") || !strings.Contains(page, "Christmas") {
		t.Fatal("no change freeze banner")
	}
	if n, _ := e.ui.AutoPRs(ctx); n != 0 || len(pulls.got) != 0 {
		t.Fatal("the version policy opened a pull request during a change freeze")
	}
	post(t, e.as(store.RoleMember), e.srv.URL+"/services/"+url.PathEscape(svc)+"/gitops/pr", url.Values{"target_id": {e.tgt.ID}, "workload_id": {"w1"}, "to": {"1.28.0"}})
	if len(pulls.got) != 1 || !strings.Contains(pulls.got[0].Body, "Opened during a change freeze") {
		t.Fatalf("pull request %+v", pulls.got)
	}
	list, _ := e.st.ListFreezes(ctx, sc, now)
	post(t, admin, e.srv.URL+"/settings/freezes/"+list[0].ID+"/delete", nil)
	if _, page := get(t, admin, e.srv.URL+"/updates", nil); strings.Contains(page, "freeze-banner") {
		t.Fatal("the banner stays once the freeze is ended")
	}
}

// Drilled into an application, the tiles offer each service's upgrade pull request per environment, as the matrix
// does; viewers see none.
func TestTilesOfferPullRequests(t *testing.T) {
	e := newUIEnv(t)
	ctx := context.Background()
	sc := e.ws.Scope()
	e.ui.pulls = &fakePulls{}
	g := store.WorkloadGitOps{
		WorkloadID: "w1", Tool: "argocd", App: "evil-prod", Repo: "https://github.com/acme/deploy.git",
		Path: "apps/evil/prod", TargetRevision: "main", Sync: "Synced", Health: "Healthy",
	}
	if err := e.st.SetWorkloadGitOps(ctx, sc, e.tgt.ID, []store.WorkloadGitOps{g}, true, time.Now()); err != nil {
		t.Fatal(err)
	}
	svc, _ := e.st.GetServiceByName(ctx, sc, e.serviceOf("evil"))
	if _, err := e.st.InsertReleases(ctx, sc, svc.ID, []string{"1.28.0"}); err != nil {
		t.Fatal(err)
	}
	m := e.as(store.RoleMember)
	_, board := get(t, m, e.srv.URL+"/tiles?group=app", nil)
	app := regexp.MustCompile(`\?app=([^&"]+)`).FindStringSubmatch(board)
	if app == nil {
		t.Fatal("no application on the board")
	}
	drill := e.srv.URL + "/tiles?group=app&app=" + app[1]
	_, page := get(t, m, drill, nil)
	if !strings.Contains(page, `class="button secondary tile-pr"`) || !strings.Contains(page, ">PR → 1.28.0</a>") ||
		!strings.Contains(page, "return=%2Ftiles") {
		t.Fatal("the tile panel offers no pull request")
	}
	if _, page := get(t, e.as(store.RoleViewer), drill, nil); strings.Contains(page, "tile-pr") {
		t.Fatal("a viewer is offered a pull request")
	}
}
