// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package gitops

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/pipozzz/goliash/internal/versions"
)

// GitHub opens pull requests through GitHub's REST API with a token that may write the repository's contents and
// pull requests (a fine-grained token on the deploy repository).
type GitHub struct {
	token string
	api   string // https://api.github.com
	host  string // github.com, or a GitHub Enterprise host
	http  *http.Client
}

// NewGitHub returns a GitHub client for token; api is empty for github.com.
func NewGitHub(token, api string) *GitHub {
	host := "github.com"
	if api == "" {
		api = "https://api.github.com"
	} else if u, err := url.Parse(api); err == nil && u.Host != "api.github.com" {
		host = u.Host // GitHub Enterprise: https://github.example.com/api/v3
	}
	return &GitHub{token: token, api: strings.TrimSuffix(api, "/"), host: host, http: &http.Client{Timeout: 30 * time.Second}}
}

// Supports says whether the repository lives on this GitHub.
func (g *GitHub) Supports(repo string) bool {
	_, _, ok := g.ownerRepo(repo)
	return ok
}

// Bump is an upgrade to open as a pull request.
type Bump struct {
	Repo      string // the repository's web address, https://github.com/acme/deploy
	Branch    string // the branch to change (the application's target revision); empty or HEAD: the default branch
	Path      string // the directory the application deploys from
	Image     string // the image's identity, versions.ParseImage(...).Repo()
	From, To  string // the tag it pins, and the one to pin
	Head      string // the new branch, e.g. goliash/checkout-1.5.0-prod
	Title     string
	Body      string // markdown
	CommitMsg string
	Reviewers []string // people (alice) and teams (@acme/payments) asked to review; best effort
	Labels    []string // labels put on the pull request; best effort
}

// ErrNoTag is returned when no file under the path pins the image at the version.
var ErrNoTag = errors.New("no file pins the image at that version")

// ErrPinned is returned when the files pin the image by digest: changing the tag alone would not change what runs.
var ErrPinned = errors.New("the image is pinned by digest")

// deployFile says whether a repository file may pin an image: Kubernetes, Kustomize, Helm and Compose YAML, Nomad
// jobs (HCL), ECS task definitions (JSON) and Terraform.
func deployFile(name string) bool {
	for _, ext := range []string{".yaml", ".yml", ".hcl", ".nomad", ".json", ".tf"} {
		if strings.HasSuffix(name, ext) {
			return true
		}
	}
	return false
}

// underPath says whether a repository file is the path, or under it ("" is the whole repository).
func underPath(file, dir string) bool {
	return dir == "" || file == dir || strings.HasPrefix(file, dir+"/")
}

// maxFiles bounds how many files under the path are read.
const maxFiles = 200

// ghPlan is what OpenBump would change, with what it needs to commit it.
type ghPlan struct {
	repoAPI, owner, base, baseSHA string
	changes                       []ghChange
}

type ghChange struct {
	path, sha string
	old, new  []byte
}

// Plan reads what OpenBump would change, and changes nothing.
func (g *GitHub) Plan(ctx context.Context, b Bump) (Plan, error) {
	gp, err := g.plan(ctx, b)
	if err != nil {
		return Plan{}, err
	}
	p := Plan{Base: gp.base}
	for _, c := range gp.changes {
		p.Changes = append(p.Changes, Change{Path: c.path, Old: c.old, New: c.new})
	}
	return p, nil
}

func (g *GitHub) plan(ctx context.Context, b Bump) (ghPlan, error) {
	pinned := false
	var changes []ghChange
	gp, err := g.walk(ctx, b, func(p, sha string, raw []byte) {
		res := versions.BumpImageTag(raw, b.Image, b.From, b.To)
		pinned = pinned || res.Pinned
		if res.Changes > 0 {
			changes = append(changes, ghChange{p, sha, raw, res.Content})
		}
	})
	if err != nil {
		return gp, err
	}
	gp.changes = changes
	if len(gp.changes) == 0 {
		if pinned {
			return gp, ErrPinned
		}
		return gp, ErrNoTag
	}
	return gp, nil
}

// Pinned reads the tags the YAML files under the application's path pin its image at, on its branch.
func (g *GitHub) Pinned(ctx context.Context, b Bump) ([]string, error) {
	var tags []string
	_, err := g.walk(ctx, b, func(_, _ string, raw []byte) { tags = appendNew(tags, versions.PinnedTags(raw, b.Image)...) })
	return tags, err
}

// walk reads the YAML files under the application's path on its branch (the repository's default branch for
// none), up to maxFiles, and hands each to visit.
func (g *GitHub) walk(ctx context.Context, b Bump, visit func(path, sha string, raw []byte)) (ghPlan, error) {
	owner, name, ok := g.ownerRepo(b.Repo)
	if !ok {
		return ghPlan{}, fmt.Errorf("%s is not a GitHub repository", b.Repo)
	}
	gp := ghPlan{repoAPI: "/repos/" + owner + "/" + name, owner: owner, base: b.Branch}
	if gp.base == "" || gp.base == "HEAD" {
		var info struct {
			DefaultBranch string `json:"default_branch"`
		}
		if err := g.do(ctx, http.MethodGet, gp.repoAPI, nil, &info); err != nil {
			return gp, err
		}
		gp.base = info.DefaultBranch
	}
	var ref struct {
		Object struct {
			SHA string `json:"sha"`
		} `json:"object"`
	}
	if err := g.do(ctx, http.MethodGet, gp.repoAPI+"/git/ref/heads/"+escapePath(gp.base), nil, &ref); err != nil {
		return gp, fmt.Errorf("branch %s: %w", gp.base, err)
	}
	gp.baseSHA = ref.Object.SHA
	var tree struct {
		Tree []struct {
			Path string `json:"path"`
			Type string `json:"type"`
			Size int    `json:"size"`
		} `json:"tree"`
	}
	if err := g.do(ctx, http.MethodGet, gp.repoAPI+"/git/trees/"+gp.baseSHA+"?recursive=1", nil, &tree); err != nil {
		return gp, err
	}
	dir := strings.Trim(path.Clean("/"+b.Path), "/")
	read := 0
	for _, f := range tree.Tree {
		if f.Type != "blob" || f.Size > 512<<10 || !deployFile(f.Path) {
			continue
		}
		if !underPath(f.Path, dir) {
			continue
		}
		if read++; read > maxFiles {
			break
		}
		var file struct {
			SHA     string `json:"sha"`
			Content string `json:"content"`
		}
		if err := g.do(ctx, http.MethodGet, gp.repoAPI+"/contents/"+escapePath(f.Path)+"?ref="+url.QueryEscape(gp.base), nil, &file); err != nil {
			return gp, err
		}
		raw, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(file.Content, "\n", ""))
		if err != nil {
			return gp, fmt.Errorf("%s: %w", f.Path, err)
		}
		visit(f.Path, file.SHA, raw)
	}
	return gp, nil
}

// OpenBump changes the tag in every YAML file under the path that pins it, on a new branch, and opens a pull
// request; it returns the pull request's address. An open pull request from the same branch is returned as is.
func (g *GitHub) OpenBump(ctx context.Context, b Bump) (string, error) {
	gp, err := g.plan(ctx, b)
	if err != nil {
		return "", err
	}
	repoAPI, owner, base := gp.repoAPI, gp.owner, gp.base
	ref := struct{ Object struct{ SHA string } }{}
	ref.Object.SHA = gp.baseSHA
	changes := gp.changes

	// A branch of our own; one left from an earlier try gets a suffix.
	head := b.Head
	for try := 0; ; try++ {
		body := map[string]string{"ref": "refs/heads/" + head, "sha": ref.Object.SHA}
		err := g.do(ctx, http.MethodPost, repoAPI+"/git/refs", body, nil)
		if err == nil {
			break
		}
		var he *httpError
		if !errors.As(err, &he) || he.status != http.StatusUnprocessableEntity || try >= 3 {
			return "", fmt.Errorf("create branch %s: %w", head, err)
		}
		if existing, ok := g.openPull(ctx, repoAPI, owner, head); ok {
			return existing, nil
		}
		head = fmt.Sprintf("%s-%d", b.Head, time.Now().Unix()+int64(try))
	}
	for _, c := range changes {
		body := map[string]string{
			"message": b.CommitMsg, "content": base64.StdEncoding.EncodeToString(c.new), "sha": c.sha, "branch": head,
		}
		if err := g.do(ctx, http.MethodPut, repoAPI+"/contents/"+escapePath(c.path), body, nil); err != nil {
			return "", fmt.Errorf("commit %s: %w", c.path, err)
		}
	}
	var pr struct {
		HTMLURL string `json:"html_url"`
		Number  int    `json:"number"`
	}
	body := map[string]any{"title": b.Title, "head": head, "base": base, "body": b.Body, "maintainer_can_modify": true}
	if err := g.do(ctx, http.MethodPost, repoAPI+"/pulls", body, &pr); err != nil {
		return "", fmt.Errorf("open the pull request: %w", err)
	}
	g.dress(ctx, repoAPI, pr.Number, b)
	return pr.HTMLURL, nil
}

// openPull finds an open pull request from head.
func (g *GitHub) openPull(ctx context.Context, repoAPI, owner, head string) (string, bool) {
	var pulls []struct {
		HTMLURL string `json:"html_url"`
	}
	if err := g.do(ctx, http.MethodGet, repoAPI+"/pulls?state=open&head="+url.QueryEscape(owner+":"+head), nil, &pulls); err != nil || len(pulls) == 0 {
		return "", false
	}
	return pulls[0].HTMLURL, true
}

type httpError struct {
	status int
	msg    string
}

func (e *httpError) Error() string { return fmt.Sprintf("the forge answered %d: %s", e.status, e.msg) }

func (g *GitHub) do(ctx context.Context, method, p string, in, out any) error {
	var body io.Reader
	if in != nil {
		raw, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, g.api+p, body)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "goliash")
	if g.token != "" {
		req.Header.Set("Authorization", "Bearer "+g.token)
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := g.http.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		var m struct {
			Message string `json:"message"`
		}
		if json.Unmarshal(raw, &m) != nil || m.Message == "" {
			m.Message = strings.TrimSpace(string(raw))
		}
		return &httpError{resp.StatusCode, m.Message}
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 32<<20)).Decode(out)
}

// ownerRepo reads owner and name from https://github.com/owner/name.
func (g *GitHub) ownerRepo(web string) (string, string, bool) {
	u, err := url.Parse(web)
	if err != nil || u.Host != g.host {
		return "", "", false
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", false
	}
	return parts[0], parts[1], true
}

func escapePath(p string) string {
	parts := strings.Split(p, "/")
	for i, s := range parts {
		parts[i] = url.PathEscape(s)
	}
	return strings.Join(parts, "/")
}

// PullState says whether a pull request this GitHub holds is open, merged or closed.
func (g *GitHub) PullState(ctx context.Context, pullURL string) (string, error) {
	u, err := url.Parse(pullURL)
	if err != nil || u.Host != g.host {
		return "", fmt.Errorf("%s is not a pull request on %s", pullURL, g.host)
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/") // owner/repo/pull/N
	if len(parts) != 4 || parts[2] != "pull" {
		return "", fmt.Errorf("%s is not a pull request address", pullURL)
	}
	var pr struct {
		State  string `json:"state"`
		Merged bool   `json:"merged"`
	}
	if err := g.do(ctx, http.MethodGet, "/repos/"+parts[0]+"/"+parts[1]+"/pulls/"+parts[3], nil, &pr); err != nil {
		return "", err
	}
	switch {
	case pr.Merged:
		return "merged", nil
	case pr.State == "closed":
		return "closed", nil
	}
	return "open", nil
}

// OpenPulls lists the repository's open pull requests (the 100 newest).
func (g *GitHub) OpenPulls(ctx context.Context, repo string) ([]Pull, error) {
	owner, name, ok := g.ownerRepo(repo)
	if !ok {
		return nil, fmt.Errorf("%s is not a GitHub repository", repo)
	}
	var list []struct {
		HTMLURL string `json:"html_url"`
		Title   string `json:"title"`
		Head    struct {
			Ref string `json:"ref"`
		} `json:"head"`
	}
	if err := g.do(ctx, http.MethodGet, "/repos/"+owner+"/"+name+"/pulls?state=open&per_page=100", nil, &list); err != nil {
		return nil, err
	}
	out := make([]Pull, 0, len(list))
	for _, p := range list {
		out = append(out, Pull{URL: p.HTMLURL, Title: p.Title, Branch: p.Head.Ref})
	}
	return out, nil
}

// PullChecks says what the checks of a pull request this GitHub holds say: failing when any failed, pending while
// any runs, passing when all passed, "" when it has none.
func (g *GitHub) PullChecks(ctx context.Context, pullURL string) (string, error) {
	u, err := url.Parse(pullURL)
	if err != nil || u.Host != g.host {
		return "", fmt.Errorf("%s is not a pull request on %s", pullURL, g.host)
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/") // owner/repo/pull/N
	if len(parts) != 4 || parts[2] != "pull" {
		return "", fmt.Errorf("%s is not a pull request address", pullURL)
	}
	repo := "/repos/" + parts[0] + "/" + parts[1]
	var pr struct {
		Head struct {
			SHA string `json:"sha"`
		} `json:"head"`
	}
	if err := g.do(ctx, http.MethodGet, repo+"/pulls/"+parts[3], nil, &pr); err != nil {
		return "", err
	}
	var runs struct {
		CheckRuns []struct {
			Status     string `json:"status"`
			Conclusion string `json:"conclusion"`
		} `json:"check_runs"`
	}
	if err := g.do(ctx, http.MethodGet, repo+"/commits/"+pr.Head.SHA+"/check-runs?per_page=100", nil, &runs); err != nil {
		return "", err
	}
	var status struct {
		State    string `json:"state"`
		Statuses []any  `json:"statuses"`
	}
	if err := g.do(ctx, http.MethodGet, repo+"/commits/"+pr.Head.SHA+"/status", nil, &status); err != nil {
		return "", err
	}
	var states []string
	for _, r := range runs.CheckRuns {
		switch {
		case r.Status != "completed":
			states = append(states, "pending")
		case r.Conclusion == "failure" || r.Conclusion == "timed_out" || r.Conclusion == "cancelled" || r.Conclusion == "action_required":
			states = append(states, "failing")
		default:
			states = append(states, "passing")
		}
	}
	if len(status.Statuses) > 0 {
		states = append(states, map[string]string{"success": "passing", "failure": "failing", "error": "failing"}[status.State])
	}
	return worstChecks(states), nil
}

// dress asks the bump's reviewers and puts its labels on a new pull request. A reviewer or label GitHub refuses
// (no such team, no access) leaves the pull request as it is: it is open either way.
func (g *GitHub) dress(ctx context.Context, repoAPI string, number int, b Bump) {
	users, teams := splitReviewers(b.Reviewers)
	if len(users)+len(teams) > 0 {
		_ = g.do(ctx, http.MethodPost, fmt.Sprintf("%s/pulls/%d/requested_reviewers", repoAPI, number),
			map[string]any{"reviewers": users, "team_reviewers": teams}, nil)
	}
	if len(b.Labels) > 0 {
		_ = g.do(ctx, http.MethodPost, fmt.Sprintf("%s/issues/%d/labels", repoAPI, number), map[string]any{"labels": b.Labels}, nil)
	}
}

// splitReviewers tells people (alice, @alice) from teams (@acme/payments: the team's slug, payments).
func splitReviewers(names []string) (users, teams []string) {
	users, teams = []string{}, []string{}
	for _, n := range names {
		n = strings.TrimPrefix(strings.TrimSpace(n), "@")
		if n == "" {
			continue
		}
		if _, team, ok := strings.Cut(n, "/"); ok {
			teams = append(teams, team)
			continue
		}
		users = append(users, n)
	}
	return users, teams
}
