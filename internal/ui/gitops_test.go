// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package ui

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
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
	got   []gitops.Bump
	state string
}

func (f *fakePulls) Supports(repo string) bool { return strings.HasPrefix(repo, "https://github.com/") }

func (f *fakePulls) PullState(context.Context, string) (string, error) { return f.state, nil }

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
	pulls.state = "merged"
	if err := e.ui.RefreshPulls(ctx); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(page(), "Pull request → 1.28.0 · merged") {
		t.Fatal("not shown as merged")
	}
	pulls.state = "closed"
	got, _ := e.st.ListGitOpsPulls(ctx, sc)
	_ = e.st.SetGitOpsPullState(ctx, sc, got[0].ID, "closed")
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
