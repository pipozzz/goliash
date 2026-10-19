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

// Gitea opens pull requests through the API of a Gitea or Forgejo instance (Codeberg included), with a token that
// may write the repository (scopes write:repository).
type Gitea struct {
	token string
	base  string // https://gitea.example.com
	host  string
	http  *http.Client
}

// NewGitea returns a client for the Gitea or Forgejo instance at base.
func NewGitea(token, base string) *Gitea {
	base = strings.TrimSuffix(base, "/")
	host := ""
	if u, err := url.Parse(base); err == nil {
		host = u.Host
	}
	return &Gitea{token: token, base: base, host: host, http: &http.Client{Timeout: 30 * time.Second}}
}

// Supports says whether the repository lives on this instance.
func (g *Gitea) Supports(repo string) bool {
	_, _, ok := g.ownerRepo(repo)
	return ok
}

func (g *Gitea) ownerRepo(repo string) (string, string, bool) {
	u, err := url.Parse(repo)
	if err != nil || g.host == "" || u.Host != g.host {
		return "", "", false
	}
	owner, name, ok := strings.Cut(strings.Trim(strings.TrimSuffix(u.Path, ".git"), "/"), "/")
	if !ok || owner == "" || name == "" || strings.Contains(name, "/") {
		return "", "", false
	}
	return owner, name, true
}

// giteaFile is a file read from the repository, with the blob SHA an update must name.
type giteaFile struct {
	path, sha string
	raw       []byte
}

// walk reads the YAML files under the application's path on its branch (the repository's default branch for
// none), up to maxFiles, and hands each to visit. It returns the repository's API path and the branch.
func (g *Gitea) walk(ctx context.Context, b Bump, visit func(f giteaFile)) (string, string, error) {
	owner, name, ok := g.ownerRepo(b.Repo)
	if !ok {
		return "", "", fmt.Errorf("%s is not on %s", b.Repo, g.base)
	}
	repo := "/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(name)
	branch := b.Branch
	if branch == "" || branch == "HEAD" {
		var info struct {
			DefaultBranch string `json:"default_branch"`
		}
		if err := g.do(ctx, http.MethodGet, repo, nil, &info); err != nil {
			return repo, "", err
		}
		branch = info.DefaultBranch
	}
	var br struct {
		Commit struct {
			ID string `json:"id"`
		} `json:"commit"`
	}
	if err := g.do(ctx, http.MethodGet, repo+"/branches/"+url.PathEscape(branch), nil, &br); err != nil {
		return repo, branch, fmt.Errorf("branch %s: %w", branch, err)
	}
	dir := strings.Trim(path.Clean("/"+b.Path), "/")
	read := 0
	for page := 1; page <= 10 && read < maxFiles; page++ {
		var tree struct {
			Tree []struct {
				Path string `json:"path"`
				Type string `json:"type"`
				Size int    `json:"size"`
			} `json:"tree"`
			Truncated bool `json:"truncated"`
		}
		q := url.Values{"recursive": {"true"}, "per_page": {"1000"}, "page": {fmt.Sprint(page)}}
		if err := g.do(ctx, http.MethodGet, repo+"/git/trees/"+br.Commit.ID+"?"+q.Encode(), nil, &tree); err != nil {
			return repo, branch, err
		}
		for _, f := range tree.Tree {
			if f.Type != "blob" || f.Size > 512<<10 || !strings.HasSuffix(f.Path, ".yaml") && !strings.HasSuffix(f.Path, ".yml") {
				continue
			}
			if dir != "" && !strings.HasPrefix(f.Path, dir+"/") {
				continue
			}
			if read++; read > maxFiles {
				break
			}
			var file struct {
				SHA     string `json:"sha"`
				Content string `json:"content"`
			}
			if err := g.do(ctx, http.MethodGet, repo+"/contents/"+escapePath(f.Path)+"?ref="+url.QueryEscape(branch), nil, &file); err != nil {
				return repo, branch, err
			}
			raw, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(file.Content, "\n", ""))
			if err != nil {
				return repo, branch, fmt.Errorf("%s: %w", f.Path, err)
			}
			visit(giteaFile{f.Path, file.SHA, raw})
		}
		if !tree.Truncated {
			break
		}
	}
	return repo, branch, nil
}

// plan finds the files that pin the image at From, with their changes.
func (g *Gitea) plan(ctx context.Context, b Bump) (string, Plan, []giteaFile, error) {
	pinned := false
	var files []giteaFile
	var changes []Change
	repo, branch, err := g.walk(ctx, b, func(f giteaFile) {
		res := versions.BumpImageTag(f.raw, b.Image, b.From, b.To)
		pinned = pinned || res.Pinned
		if res.Changes > 0 {
			files = append(files, giteaFile{f.path, f.sha, res.Content})
			changes = append(changes, Change{Path: f.path, Old: f.raw, New: res.Content})
		}
	})
	p := Plan{Base: branch, Changes: changes}
	if err != nil {
		return repo, p, nil, err
	}
	if len(changes) == 0 {
		if pinned {
			return repo, p, nil, ErrPinned
		}
		return repo, p, nil, ErrNoTag
	}
	return repo, p, files, nil
}

// Plan reads what OpenBump would change, and changes nothing.
func (g *Gitea) Plan(ctx context.Context, b Bump) (Plan, error) {
	_, p, _, err := g.plan(ctx, b)
	return p, err
}

// Pinned reads the tags the YAML files under the application's path pin its image at, on its branch.
func (g *Gitea) Pinned(ctx context.Context, b Bump) ([]string, error) {
	var tags []string
	_, _, err := g.walk(ctx, b, func(f giteaFile) { tags = appendNew(tags, versions.PinnedTags(f.raw, b.Image)...) })
	return tags, err
}

// OpenBump changes the tag in every YAML file under the path that pins it, in one commit on a new branch, and
// opens a pull request; it returns the pull request's address. An open pull request from the same branch is
// returned as is.
func (g *Gitea) OpenBump(ctx context.Context, b Bump) (string, error) {
	repo, p, files, err := g.plan(ctx, b)
	if err != nil {
		return "", err
	}
	ops := make([]map[string]string, 0, len(files))
	for _, f := range files {
		ops = append(ops, map[string]string{
			"operation": "update", "path": f.path, "sha": f.sha, "content": base64.StdEncoding.EncodeToString(f.raw),
		})
	}
	head := b.Head
	for try := 0; ; try++ {
		commit := map[string]any{"branch": p.Base, "new_branch": head, "message": b.CommitMsg, "files": ops}
		err := g.do(ctx, http.MethodPost, repo+"/contents", commit, nil)
		if err == nil {
			break
		}
		var he *httpError
		if !errors.As(err, &he) || he.status != http.StatusUnprocessableEntity && he.status != http.StatusConflict || try >= 3 {
			return "", fmt.Errorf("commit on %s: %w", head, err) // 422 or 409: the branch exists
		}
		var open []struct {
			HTMLURL string `json:"html_url"`
			Head    struct {
				Ref string `json:"ref"`
			} `json:"head"`
		}
		if g.do(ctx, http.MethodGet, repo+"/pulls?state=open&limit=50", nil, &open) == nil {
			for _, pr := range open {
				if pr.Head.Ref == head {
					return pr.HTMLURL, nil
				}
			}
		}
		head = fmt.Sprintf("%s-%d", b.Head, time.Now().Unix()+int64(try))
	}
	var pr struct {
		HTMLURL string `json:"html_url"`
	}
	if err := g.do(ctx, http.MethodPost, repo+"/pulls", map[string]string{"head": head, "base": p.Base, "title": b.Title, "body": b.Body}, &pr); err != nil {
		return "", fmt.Errorf("open the pull request: %w", err)
	}
	return pr.HTMLURL, nil
}

// PullState says whether a pull request on this instance is open, merged or closed.
func (g *Gitea) PullState(ctx context.Context, pullURL string) (string, error) {
	repo, index, ok := strings.Cut(pullURL, "/pulls/")
	owner, name, ok2 := g.ownerRepo(repo)
	if !ok || !ok2 || index == "" || strings.Contains(index, "/") {
		return "", fmt.Errorf("%s is not a pull request on %s", pullURL, g.base)
	}
	var pr struct {
		State  string `json:"state"` // open, closed
		Merged bool   `json:"merged"`
	}
	if err := g.do(ctx, http.MethodGet, "/repos/"+url.PathEscape(owner)+"/"+url.PathEscape(name)+"/pulls/"+url.PathEscape(index), nil, &pr); err != nil {
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

func (g *Gitea) do(ctx context.Context, method, p string, in, out any) error {
	var body io.Reader
	if in != nil {
		raw, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, g.base+"/api/v1"+p, body)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "goliash")
	req.Header.Set("Accept", "application/json")
	if g.token != "" {
		req.Header.Set("Authorization", "token "+g.token)
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
		msg := strings.TrimSpace(string(raw))
		if json.Unmarshal(raw, &m) == nil && m.Message != "" {
			msg = m.Message
		}
		return &httpError{resp.StatusCode, msg}
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 32<<20)).Decode(out)
}
