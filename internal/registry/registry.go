// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: Apache-2.0

package registry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
)

// maxPages bounds tag listing for repositories with very many tags.
const maxPages = 50

// Credentials authenticate to a registry. Empty means anonymous.
type Credentials struct {
	Username string
	Password string
}

// ParseCredentials reads "username:password" (or a bare token, used as the password).
func ParseCredentials(s string) Credentials {
	if u, p, ok := strings.Cut(s, ":"); ok {
		return Credentials{Username: u, Password: p}
	}
	return Credentials{Password: s}
}

// Client lists tags through the OCI Distribution API (GET /v2/<repo>/tags/list),
// handling the bearer token challenge used by Docker Hub, GHCR, Quay, Harbor and
// GitLab, and Link-header pagination. It only reads.
type Client struct {
	HTTP *http.Client
	// Scheme is "https" except in tests.
	Scheme string

	mu     sync.Mutex
	tokens map[string]cachedToken // realm+scope+user -> token
}

type cachedToken struct {
	token   string
	expires time.Time
}

// New returns a client with sensible timeouts.
func New() *Client {
	return &Client{HTTP: &http.Client{Timeout: 30 * time.Second}, Scheme: "https", tokens: map[string]cachedToken{}}
}

// ErrUnauthorized means the registry refused the credentials (or anonymous access).
var ErrUnauthorized = errors.New("registry denied access")

// ListTags returns every tag of repository ("ghcr.io/acme/app", "docker.io/library/nginx").
func (c *Client) ListTags(ctx context.Context, repository string, creds Credentials) ([]string, error) {
	host, repo, err := splitRepository(repository)
	if err != nil {
		return nil, err
	}
	next := fmt.Sprintf("%s://%s/v2/%s/tags/list?n=1000", c.Scheme, host, repo)
	var tags []string
	for page := 0; next != "" && page < maxPages; page++ {
		body, link, err := c.get(ctx, next, repo, creds, "application/json")
		if err != nil {
			return nil, err
		}
		var out struct {
			Tags []string `json:"tags"`
		}
		if err := json.Unmarshal(body, &out); err != nil {
			return nil, fmt.Errorf("tags of %s: %w", repository, err)
		}
		tags = append(tags, out.Tags...)
		next = resolveLink(next, link)
	}
	return tags, nil
}

// splitRepository returns the API host and the repository path on it.
func splitRepository(repository string) (host, repo string, err error) {
	host, repo, ok := strings.Cut(repository, "/")
	if !ok {
		return "", "", fmt.Errorf("repository %q has no registry host", repository)
	}
	if host == "docker.io" {
		host = "registry-1.docker.io"
	}
	return host, repo, nil
}

func (c *Client) get(ctx context.Context, rawURL, repo string, creds Credentials, accept string) ([]byte, string, error) {
	resp, err := c.request(ctx, http.MethodGet, rawURL, repo, creds, accept)
	if err != nil {
		return nil, "", err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return nil, "", err
	}
	if err := statusError(resp, body); err != nil {
		return nil, "", err
	}
	return body, resp.Header.Get("Link"), nil
}

// head returns the response headers of a HEAD request. Docker Hub does not count
// HEAD requests against its pull limits.
func (c *Client) head(ctx context.Context, rawURL, repo string, creds Credentials, accept string) (http.Header, error) {
	resp, err := c.request(ctx, http.MethodHead, rawURL, repo, creds, accept)
	if err != nil {
		return nil, err
	}
	_ = resp.Body.Close()
	if err := statusError(resp, nil); err != nil {
		return nil, err
	}
	return resp.Header, nil
}

func statusError(resp *http.Response, body []byte) error {
	switch resp.StatusCode {
	case http.StatusOK:
		return nil
	case http.StatusUnauthorized, http.StatusForbidden:
		return fmt.Errorf("%w (%d)", ErrUnauthorized, resp.StatusCode)
	case http.StatusTooManyRequests:
		return errors.New("registry rate limit reached (429)")
	}
	return fmt.Errorf("registry answered %d: %s", resp.StatusCode, strings.TrimSpace(string(body[:min(len(body), 200)])))
}

// request sends method to rawURL, answering an authentication challenge once.
func (c *Client) request(ctx context.Context, method, rawURL, repo string, creds Credentials, accept string) (*http.Response, error) {
	resp, err := c.do(ctx, method, rawURL, "", creds, accept)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusUnauthorized {
		challenge := resp.Header.Get("WWW-Authenticate")
		_ = resp.Body.Close()
		auth, err := c.authorize(ctx, challenge, repo, creds)
		if err != nil {
			return nil, err
		}
		if resp, err = c.do(ctx, method, rawURL, auth, creds, accept); err != nil {
			return nil, err
		}
	}
	return resp, nil
}

func (c *Client) do(ctx context.Context, method, rawURL, auth string, creds Credentials, accept string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", accept)
	switch {
	case auth != "":
		req.Header.Set("Authorization", auth)
	case creds.Username != "":
		req.SetBasicAuth(creds.Username, creds.Password)
	}
	return c.HTTP.Do(req)
}

var challengeParam = regexp.MustCompile(`(\w+)="([^"]*)"`)

// authorize answers a WWW-Authenticate challenge. Bearer challenges fetch a pull
// token from the realm; Basic challenges use the credentials directly.
func (c *Client) authorize(ctx context.Context, challenge, repo string, creds Credentials) (string, error) {
	scheme, params, _ := strings.Cut(challenge, " ")
	if strings.EqualFold(scheme, "basic") {
		if creds.Password == "" {
			return "", fmt.Errorf("%w: credentials required", ErrUnauthorized)
		}
		req, _ := http.NewRequest(http.MethodGet, "/", nil) //nolint:noctx // only used to encode the header
		req.SetBasicAuth(creds.Username, creds.Password)
		return req.Header.Get("Authorization"), nil
	}
	if !strings.EqualFold(scheme, "bearer") {
		return "", fmt.Errorf("unsupported registry auth %q", challenge)
	}
	p := map[string]string{}
	for _, m := range challengeParam.FindAllStringSubmatch(params, -1) {
		p[m[1]] = m[2]
	}
	if p["realm"] == "" {
		return "", fmt.Errorf("registry challenge without realm: %q", challenge)
	}
	scope := p["scope"]
	if scope == "" {
		scope = "repository:" + repo + ":pull"
	}
	key := p["realm"] + "|" + scope + "|" + creds.Username
	c.mu.Lock()
	if t, ok := c.tokens[key]; ok && time.Now().Before(t.expires) {
		c.mu.Unlock()
		return "Bearer " + t.token, nil
	}
	c.mu.Unlock()

	q := url.Values{"scope": {scope}}
	if p["service"] != "" {
		q.Set("service", p["service"])
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p["realm"]+"?"+q.Encode(), nil)
	if err != nil {
		return "", err
	}
	if creds.Password != "" {
		req.SetBasicAuth(creds.Username, creds.Password)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return "", fmt.Errorf("registry token: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%w: token endpoint answered %d", ErrUnauthorized, resp.StatusCode)
	}
	var tok struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tok); err != nil {
		return "", fmt.Errorf("registry token: %w", err)
	}
	token := tok.Token
	if token == "" {
		token = tok.AccessToken
	}
	if tok.ExpiresIn <= 0 {
		tok.ExpiresIn = 60
	}
	c.mu.Lock()
	c.tokens[key] = cachedToken{token: token, expires: time.Now().Add(time.Duration(tok.ExpiresIn)*time.Second - 10*time.Second)}
	c.mu.Unlock()
	return "Bearer " + token, nil
}

// resolveLink returns the next page URL from a Link header (`</v2/…?last=x&n=1000>; rel="next"`).
func resolveLink(current, link string) string {
	if link == "" {
		return ""
	}
	start, end := strings.Index(link, "<"), strings.Index(link, ">")
	if start < 0 || end <= start || !strings.Contains(link, `rel="next"`) {
		return ""
	}
	base, err := url.Parse(current)
	if err != nil {
		return ""
	}
	ref, err := url.Parse(link[start+1 : end])
	if err != nil {
		return ""
	}
	return base.ResolveReference(ref).String()
}
