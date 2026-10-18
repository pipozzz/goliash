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
	http  *http.Client
}

// NewGitHub returns a GitHub client for token; api is empty for github.com.
func NewGitHub(token, api string) *GitHub {
	if api == "" {
		api = "https://api.github.com"
	}
	return &GitHub{token: token, api: strings.TrimSuffix(api, "/"), http: &http.Client{Timeout: 30 * time.Second}}
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
}

// ErrNoTag is returned when no file under the path pins the image at the version.
var ErrNoTag = errors.New("no file pins the image at that version")

// ErrPinned is returned when the files pin the image by digest: changing the tag alone would not change what runs.
var ErrPinned = errors.New("the image is pinned by digest")

// maxFiles bounds how many YAML files under the path are read.
const maxFiles = 200

// OpenBump changes the tag in every YAML file under the path that pins it, on a new branch, and opens a pull
// request; it returns the pull request's address. An open pull request from the same branch is returned as is.
func (g *GitHub) OpenBump(ctx context.Context, b Bump) (string, error) {
	owner, name, ok := ownerRepo(b.Repo)
	if !ok {
		return "", fmt.Errorf("%s is not a GitHub repository", b.Repo)
	}
	repoAPI := "/repos/" + owner + "/" + name
	base := b.Branch
	if base == "" || base == "HEAD" {
		var info struct {
			DefaultBranch string `json:"default_branch"`
		}
		if err := g.do(ctx, http.MethodGet, repoAPI, nil, &info); err != nil {
			return "", err
		}
		base = info.DefaultBranch
	}
	var ref struct {
		Object struct {
			SHA string `json:"sha"`
		} `json:"object"`
	}
	if err := g.do(ctx, http.MethodGet, repoAPI+"/git/ref/heads/"+escapePath(base), nil, &ref); err != nil {
		return "", fmt.Errorf("branch %s: %w", base, err)
	}
	var tree struct {
		Tree []struct {
			Path string `json:"path"`
			Type string `json:"type"`
			Size int    `json:"size"`
		} `json:"tree"`
	}
	if err := g.do(ctx, http.MethodGet, repoAPI+"/git/trees/"+ref.Object.SHA+"?recursive=1", nil, &tree); err != nil {
		return "", err
	}
	dir := strings.Trim(path.Clean("/"+b.Path), "/")
	type change struct {
		path, sha string
		content   []byte
	}
	var changes []change
	pinned, read := false, 0
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
		if err := g.do(ctx, http.MethodGet, repoAPI+"/contents/"+escapePath(f.Path)+"?ref="+url.QueryEscape(base), nil, &file); err != nil {
			return "", err
		}
		raw, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(file.Content, "\n", ""))
		if err != nil {
			return "", fmt.Errorf("%s: %w", f.Path, err)
		}
		res := versions.BumpImageTag(raw, b.Image, b.From, b.To)
		pinned = pinned || res.Pinned
		if res.Changes > 0 {
			changes = append(changes, change{f.Path, file.SHA, res.Content})
		}
	}
	if len(changes) == 0 {
		if pinned {
			return "", ErrPinned
		}
		return "", ErrNoTag
	}

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
			"message": b.CommitMsg, "content": base64.StdEncoding.EncodeToString(c.content), "sha": c.sha, "branch": head,
		}
		if err := g.do(ctx, http.MethodPut, repoAPI+"/contents/"+escapePath(c.path), body, nil); err != nil {
			return "", fmt.Errorf("commit %s: %w", c.path, err)
		}
	}
	var pr struct {
		HTMLURL string `json:"html_url"`
	}
	body := map[string]any{"title": b.Title, "head": head, "base": base, "body": b.Body, "maintainer_can_modify": true}
	if err := g.do(ctx, http.MethodPost, repoAPI+"/pulls", body, &pr); err != nil {
		return "", fmt.Errorf("open the pull request: %w", err)
	}
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

func (e *httpError) Error() string { return fmt.Sprintf("GitHub answered %d: %s", e.status, e.msg) }

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
func ownerRepo(web string) (string, string, bool) {
	u, err := url.Parse(web)
	if err != nil || u.Host != "github.com" {
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
