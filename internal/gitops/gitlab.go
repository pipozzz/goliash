// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package gitops

import (
	"bytes"
	"context"
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

// GitLab opens merge requests through GitLab's REST API (gitlab.com or a self-hosted instance) with a token that
// may write the repository (a project or group access token with the api scope and the Developer role).
type GitLab struct {
	token string
	base  string // https://gitlab.com
	host  string
	http  *http.Client
}

// NewGitLab returns a GitLab client for the instance at base (empty for gitlab.com).
func NewGitLab(token, base string) *GitLab {
	if base == "" {
		base = "https://gitlab.com"
	}
	base = strings.TrimSuffix(base, "/")
	u, _ := url.Parse(base)
	host := ""
	if u != nil {
		host = u.Host
	}
	return &GitLab{token: token, base: base, host: host, http: &http.Client{Timeout: 30 * time.Second}}
}

// Supports says whether the repository lives on this GitLab.
func (g *GitLab) Supports(repo string) bool {
	u, err := url.Parse(repo)
	return err == nil && u.Host == g.host && strings.Count(strings.Trim(u.Path, "/"), "/") >= 1
}

// Plan reads what OpenBump would change, and changes nothing.
func (g *GitLab) Plan(ctx context.Context, b Bump) (Plan, error) {
	_, p, err := g.plan(ctx, b)
	return p, err
}

func (g *GitLab) plan(ctx context.Context, b Bump) (string, Plan, error) {
	var project string
	var p Plan
	var e edits
	for _, pin := range b.Pins() {
		changed, pinned := false, false
		var err error
		project, p, err = g.walk(ctx, b.at(pin), func(path string, raw []byte) {
			c, d := e.apply(path, "", raw, pin)
			changed, pinned = changed || c, pinned || d
		})
		if err != nil {
			return project, p, err
		}
		if !changed {
			return project, p, pinErr(b, pin, pinned)
		}
	}
	for _, f := range e.list() {
		p.Changes = append(p.Changes, Change{Path: f.path, Old: f.old, New: f.new})
	}
	return project, p, nil
}

// Pinned reads the tags the YAML files under the application's path pin its image at, on its branch.
func (g *GitLab) Pinned(ctx context.Context, b Bump) ([]string, error) {
	var tags []string
	_, _, err := g.walk(ctx, b, func(_ string, raw []byte) { tags = appendNew(tags, versions.PinnedTags(raw, b.Image)...) })
	return tags, err
}

// walk reads the YAML files under the application's path on its branch (the project's default branch for none),
// up to maxFiles, and hands each to visit.
func (g *GitLab) walk(ctx context.Context, b Bump, visit func(path string, raw []byte)) (string, Plan, error) {
	if !g.Supports(b.Repo) {
		return "", Plan{}, fmt.Errorf("%s is not on %s", b.Repo, g.base)
	}
	u, _ := url.Parse(b.Repo)
	project := "/projects/" + url.PathEscape(strings.Trim(u.Path, "/"))
	p := Plan{Base: b.Branch}
	if p.Base == "" || p.Base == "HEAD" {
		var info struct {
			DefaultBranch string `json:"default_branch"`
		}
		if err := g.do(ctx, http.MethodGet, project, nil, &info); err != nil {
			return project, p, err
		}
		p.Base = info.DefaultBranch
	}
	dir := strings.Trim(path.Clean("/"+b.Path), "/")
	read := 0
	for page := 1; page <= 10 && read < maxFiles; page++ {
		var tree []struct {
			Path string `json:"path"`
			Type string `json:"type"`
		}
		q := url.Values{"ref": {p.Base}, "recursive": {"true"}, "per_page": {"100"}, "page": {fmt.Sprint(page)}}
		if dir != "" {
			q.Set("path", dir)
			if deployFile(dir) { // a file: list its directory, and keep only the file
				q.Del("path")
				if d := path.Dir(dir); d != "." {
					q.Set("path", d)
				}
			}
		}
		if err := g.do(ctx, http.MethodGet, project+"/repository/tree?"+q.Encode(), nil, &tree); err != nil {
			return project, p, fmt.Errorf("branch %s: %w", p.Base, err)
		}
		for _, f := range tree {
			if f.Type != "blob" || !deployFile(f.Path) || !underPath(f.Path, dir) {
				continue
			}
			if read++; read > maxFiles {
				break
			}
			raw, err := g.raw(ctx, project+"/repository/files/"+url.PathEscape(f.Path)+"/raw?ref="+url.QueryEscape(p.Base))
			if err != nil {
				return project, p, err
			}
			visit(f.Path, raw)
		}
		if len(tree) < 100 {
			break
		}
	}
	return project, p, nil
}

// OpenBump changes the tag in every YAML file under the path that pins it, in one commit on a new branch, and
// opens a merge request; it returns the merge request's address. An open merge request from the same branch is
// returned as is.
func (g *GitLab) OpenBump(ctx context.Context, b Bump) (string, error) {
	project, p, err := g.plan(ctx, b)
	if err != nil {
		return "", err
	}
	base := p.Base
	type change struct {
		path    string
		content []byte
	}
	var changes []change
	for _, c := range p.Changes {
		changes = append(changes, change{c.Path, c.New})
	}

	actions := make([]map[string]string, 0, len(changes))
	for _, c := range changes {
		actions = append(actions, map[string]string{"action": "update", "file_path": c.path, "content": string(c.content)})
	}
	head := b.Head
	for try := 0; ; try++ {
		commit := map[string]any{"branch": head, "start_branch": base, "commit_message": b.CommitMsg, "actions": actions}
		err := g.do(ctx, http.MethodPost, project+"/repository/commits", commit, nil)
		if err == nil {
			break
		}
		var he *httpError
		if !errors.As(err, &he) || he.status != http.StatusBadRequest || try >= 3 { // 400: the branch exists
			return "", fmt.Errorf("commit on %s: %w", head, err)
		}
		var open []struct {
			WebURL string `json:"web_url"`
		}
		q := url.Values{"source_branch": {head}, "state": {"opened"}}
		if g.do(ctx, http.MethodGet, project+"/merge_requests?"+q.Encode(), nil, &open) == nil && len(open) > 0 {
			return open[0].WebURL, nil
		}
		head = fmt.Sprintf("%s-%d", b.Head, time.Now().Unix()+int64(try))
	}
	var mr struct {
		WebURL string `json:"web_url"`
	}
	body := map[string]any{"source_branch": head, "target_branch": base, "title": b.Title, "description": b.Body, "remove_source_branch": true}
	if len(b.Labels) > 0 {
		body["labels"] = strings.Join(b.Labels, ",")
	}
	if ids := g.userIDs(ctx, b.Reviewers); len(ids) > 0 {
		body["reviewer_ids"] = ids
	}
	if err := g.do(ctx, http.MethodPost, project+"/merge_requests", body, &mr); err != nil {
		return "", fmt.Errorf("open the merge request: %w", err)
	}
	return mr.WebURL, nil
}

func (g *GitLab) request(ctx context.Context, method, p string, in any) (*http.Response, error) {
	var body io.Reader
	if in != nil {
		raw, err := json.Marshal(in)
		if err != nil {
			return nil, err
		}
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, g.base+"/api/v4"+p, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "goliash")
	if g.token != "" {
		req.Header.Set("PRIVATE-TOKEN", g.token)
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := g.http.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 300 {
		defer func() { _ = resp.Body.Close() }()
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		var m struct {
			Message any `json:"message"`
		}
		msg := strings.TrimSpace(string(raw))
		if json.Unmarshal(raw, &m) == nil && m.Message != nil {
			msg = fmt.Sprint(m.Message)
		}
		return nil, &httpError{resp.StatusCode, msg}
	}
	return resp, nil
}

func (g *GitLab) do(ctx context.Context, method, p string, in, out any) error {
	resp, err := g.request(ctx, method, p, in)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if out == nil {
		return nil
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 32<<20)).Decode(out)
}

func (g *GitLab) raw(ctx context.Context, p string) ([]byte, error) {
	resp, err := g.request(ctx, http.MethodGet, p, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	return io.ReadAll(io.LimitReader(resp.Body, 512<<10))
}

// PullState says whether a merge request on this GitLab is open, merged or closed.
func (g *GitLab) PullState(ctx context.Context, mrURL string) (string, error) {
	u, err := url.Parse(mrURL)
	if err != nil || u.Host != g.host {
		return "", fmt.Errorf("%s is not a merge request on %s", mrURL, g.base)
	}
	project, iid, ok := strings.Cut(strings.Trim(u.Path, "/"), "/-/merge_requests/")
	if !ok || iid == "" {
		return "", fmt.Errorf("%s is not a merge request address", mrURL)
	}
	var mr struct {
		State string `json:"state"` // opened, merged, closed, locked
	}
	if err := g.do(ctx, http.MethodGet, "/projects/"+url.PathEscape(project)+"/merge_requests/"+url.PathEscape(iid), nil, &mr); err != nil {
		return "", err
	}
	switch mr.State {
	case "merged", "closed":
		return mr.State, nil
	}
	return "open", nil
}

// OpenPulls lists the project's open merge requests (the 100 newest).
func (g *GitLab) OpenPulls(ctx context.Context, repo string) ([]Pull, error) {
	if !g.Supports(repo) {
		return nil, fmt.Errorf("%s is not on %s", repo, g.base)
	}
	u, _ := url.Parse(repo)
	var list []struct {
		WebURL       string `json:"web_url"`
		Title        string `json:"title"`
		SourceBranch string `json:"source_branch"`
	}
	project := "/projects/" + url.PathEscape(strings.Trim(strings.TrimSuffix(u.Path, ".git"), "/"))
	if err := g.do(ctx, http.MethodGet, project+"/merge_requests?state=opened&per_page=100", nil, &list); err != nil {
		return nil, err
	}
	out := make([]Pull, 0, len(list))
	for _, p := range list {
		out = append(out, Pull{URL: p.WebURL, Title: p.Title, Branch: p.SourceBranch})
	}
	return out, nil
}

// PullChecks says what the pipeline of a merge request on this GitLab says.
func (g *GitLab) PullChecks(ctx context.Context, mrURL string) (string, error) {
	u, err := url.Parse(mrURL)
	if err != nil || u.Host != g.host {
		return "", fmt.Errorf("%s is not a merge request on %s", mrURL, g.base)
	}
	project, iid, ok := strings.Cut(strings.Trim(u.Path, "/"), "/-/merge_requests/")
	if !ok || iid == "" {
		return "", fmt.Errorf("%s is not a merge request address", mrURL)
	}
	var mr struct {
		HeadPipeline *struct {
			Status string `json:"status"`
		} `json:"head_pipeline"`
	}
	if err := g.do(ctx, http.MethodGet, "/projects/"+url.PathEscape(project)+"/merge_requests/"+url.PathEscape(iid), nil, &mr); err != nil {
		return "", err
	}
	if mr.HeadPipeline == nil {
		return "", nil
	}
	switch mr.HeadPipeline.Status {
	case "success":
		return "passing", nil
	case "failed", "canceled":
		return "failing", nil
	case "skipped", "manual":
		return "", nil
	}
	return "pending", nil
}

// userIDs are the GitLab user IDs of the reviewers named (alice, @alice); groups and unknown names are left out.
func (g *GitLab) userIDs(ctx context.Context, names []string) []int {
	var ids []int
	for _, n := range names {
		n = strings.TrimPrefix(strings.TrimSpace(n), "@")
		if n == "" || strings.Contains(n, "/") {
			continue
		}
		var users []struct {
			ID int `json:"id"`
		}
		if g.do(ctx, http.MethodGet, "/users?username="+url.QueryEscape(n), nil, &users) == nil && len(users) == 1 {
			ids = append(ids, users[0].ID)
		}
	}
	return ids
}
