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

// OpenBump changes the tag in every YAML file under the path that pins it, in one commit on a new branch, and
// opens a merge request; it returns the merge request's address. An open merge request from the same branch is
// returned as is.
func (g *GitLab) OpenBump(ctx context.Context, b Bump) (string, error) {
	if !g.Supports(b.Repo) {
		return "", fmt.Errorf("%s is not on %s", b.Repo, g.base)
	}
	u, _ := url.Parse(b.Repo)
	project := "/projects/" + url.PathEscape(strings.Trim(u.Path, "/"))
	base := b.Branch
	if base == "" || base == "HEAD" {
		var info struct {
			DefaultBranch string `json:"default_branch"`
		}
		if err := g.do(ctx, http.MethodGet, project, nil, &info); err != nil {
			return "", err
		}
		base = info.DefaultBranch
	}
	dir := strings.Trim(path.Clean("/"+b.Path), "/")
	type change struct {
		path    string
		content []byte
	}
	var changes []change
	pinned, read := false, 0
	for page := 1; page <= 10 && read < maxFiles; page++ {
		var tree []struct {
			Path string `json:"path"`
			Type string `json:"type"`
		}
		q := url.Values{"ref": {base}, "recursive": {"true"}, "per_page": {"100"}, "page": {fmt.Sprint(page)}}
		if dir != "" {
			q.Set("path", dir)
		}
		if err := g.do(ctx, http.MethodGet, project+"/repository/tree?"+q.Encode(), nil, &tree); err != nil {
			return "", fmt.Errorf("branch %s: %w", base, err)
		}
		for _, f := range tree {
			if f.Type != "blob" || !strings.HasSuffix(f.Path, ".yaml") && !strings.HasSuffix(f.Path, ".yml") {
				continue
			}
			if read++; read > maxFiles {
				break
			}
			raw, err := g.raw(ctx, project+"/repository/files/"+url.PathEscape(f.Path)+"/raw?ref="+url.QueryEscape(base))
			if err != nil {
				return "", err
			}
			res := versions.BumpImageTag(raw, b.Image, b.From, b.To)
			pinned = pinned || res.Pinned
			if res.Changes > 0 {
				changes = append(changes, change{f.Path, res.Content})
			}
		}
		if len(tree) < 100 {
			break
		}
	}
	if len(changes) == 0 {
		if pinned {
			return "", ErrPinned
		}
		return "", ErrNoTag
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
