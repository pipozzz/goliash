// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

// goliash login signs the CLI in through the browser, with the server's OAuth (the same sign-in MCP clients
// use): no API token to create or copy. The tokens are kept in the user's config directory, readable by them
// only, per server; goliash ask and goliash mcp use them, renewing them as they expire.

// loginTimeout is how long goliash login waits for the browser.
const loginTimeout = 5 * time.Minute

// credential is one server's sign-in.
type credential struct {
	ClientID     string    `json:"client_id"`
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token"`
	ExpiresAt    time.Time `json:"expires_at"`
	TokenURL     string    `json:"token_endpoint"`
	RevokeURL    string    `json:"revocation_endpoint,omitempty"`
}

// credentials are every server's, and the one signed in to last.
type credentials struct {
	Default string                 `json:"default"`
	Servers map[string]*credential `json:"servers"`
}

func credentialsPath() (string, error) {
	if p := os.Getenv("GOLIASH_CREDENTIALS"); p != "" {
		return p, nil
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "goliash", "credentials.json"), nil
}

func loadCredentials() (credentials, error) {
	c := credentials{Servers: map[string]*credential{}}
	p, err := credentialsPath()
	if err != nil {
		return c, err
	}
	b, err := os.ReadFile(p) //nolint:gosec // the user's own config file
	if errors.Is(err, os.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return c, err
	}
	if err := json.Unmarshal(b, &c); err != nil {
		return c, fmt.Errorf("%s: %w", p, err)
	}
	if c.Servers == nil {
		c.Servers = map[string]*credential{}
	}
	return c, nil
}

func saveCredentials(c credentials) error {
	p, err := credentialsPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

func normServer(s string) string { return strings.TrimRight(strings.TrimSpace(s), "/") }

// defaultServer is GOLIASH_URL, else the server signed in to last, else a local one.
func defaultServer() string {
	if s := os.Getenv("GOLIASH_URL"); s != "" {
		return s
	}
	if c, err := loadCredentials(); err == nil && c.Default != "" {
		return c.Default
	}
	return "http://localhost:8080"
}

// tokenFor gives the token to call server with: the -token flag or GOLIASH_TOKEN, else the signed-in one,
// renewed when it is about to expire. It returns nil when there is neither.
func tokenFor(server, flagToken string) func(context.Context) (string, error) {
	if flagToken != "" {
		return func(context.Context) (string, error) { return flagToken, nil }
	}
	server = normServer(server)
	c, err := loadCredentials()
	if err != nil || c.Servers[server] == nil {
		return nil
	}
	var mu sync.Mutex
	return func(ctx context.Context) (string, error) {
		mu.Lock()
		defer mu.Unlock()
		c, err := loadCredentials() // another goliash may have renewed it meanwhile
		if err != nil {
			return "", err
		}
		cr := c.Servers[server]
		if cr == nil {
			return "", fmt.Errorf("not signed in to %s: goliash login %s", server, server)
		}
		if time.Until(cr.ExpiresAt) > time.Minute {
			return cr.AccessToken, nil
		}
		tok, err := tokenRequest(ctx, cr.TokenURL, url.Values{
			"grant_type": {"refresh_token"}, "refresh_token": {cr.RefreshToken}, "client_id": {cr.ClientID},
		})
		if err != nil {
			return "", fmt.Errorf("the sign-in to %s ended (%w): goliash login %s", server, err, server)
		}
		cr.AccessToken, cr.RefreshToken, cr.ExpiresAt = tok.AccessToken, tok.RefreshToken, tok.expiresAt()
		if err := saveCredentials(c); err != nil {
			return "", err
		}
		return cr.AccessToken, nil
	}
}

type tokenAnswer struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
	Error        string `json:"error"`
	Description  string `json:"error_description"`
}

func (t tokenAnswer) expiresAt() time.Time {
	return time.Now().Add(time.Duration(t.ExpiresIn) * time.Second)
}

func tokenRequest(ctx context.Context, tokenURL string, form url.Values) (tokenAnswer, error) {
	var t tokenAnswer
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return t, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return t, err
	}
	defer func() { _ = resp.Body.Close() }()
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&t); err != nil {
		return t, fmt.Errorf("token endpoint answered %d", resp.StatusCode)
	}
	if resp.StatusCode != http.StatusOK || t.AccessToken == "" {
		return t, fmt.Errorf("%s: %s", t.Error, t.Description)
	}
	return t, nil
}

// serverMetadata is the authorization server's endpoints (RFC 8414).
type serverMetadata struct {
	Issuer       string `json:"issuer"`
	Authorize    string `json:"authorization_endpoint"`
	Token        string `json:"token_endpoint"`
	Register     string `json:"registration_endpoint"`
	Revoke       string `json:"revocation_endpoint"`
	IssInAnswers bool   `json:"authorization_response_iss_parameter_supported"`
}

func getJSON(ctx context.Context, u string, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil) //nolint:gosec // the server the user names
	if err != nil {
		return err
	}
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req) //nolint:gosec // the server the user names
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s answered %d", u, resp.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(v)
}

func randomString(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// openBrowser tries the system's way of opening a URL; the URL is printed anyway.
func openBrowser(ctx context.Context, u string) {
	var cmd *exec.Cmd
	// The URL is one argument, never a shell's input.
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.CommandContext(ctx, "open", u) //nolint:gosec // see above
	case "windows":
		cmd = exec.CommandContext(ctx, "rundll32", "url.dll,FileProtocolHandler", u) //nolint:gosec // see above
	default:
		cmd = exec.CommandContext(ctx, "xdg-open", u) //nolint:gosec // see above
	}
	_ = cmd.Start()
}

func loginCmd(ctx context.Context, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("login", flag.ContinueOnError)
	noBrowser := fs.Bool("no-browser", false, "print the address to open instead of opening a browser")
	if err := fs.Parse(args); err != nil {
		return err
	}
	server := os.Getenv("GOLIASH_URL")
	if fs.NArg() > 0 {
		server = fs.Arg(0)
	}
	server = normServer(server)
	if server == "" {
		return errors.New("which server? goliash login https://goliash.example.com")
	}
	var md serverMetadata
	if err := getJSON(ctx, server+"/.well-known/oauth-authorization-server", &md); err != nil {
		return fmt.Errorf("%s does not offer signing in (Goliash 1.55 or newer does): %w", server, err)
	}
	if md.Issuer != server {
		return fmt.Errorf("%s says it is %s: use that address (it is GOLIASH_PUBLIC_URL)", server, md.Issuer)
	}

	ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	defer func() { _ = ln.Close() }()
	redirect := fmt.Sprintf("http://127.0.0.1:%d/callback", ln.Addr().(*net.TCPAddr).Port)

	host, _ := os.Hostname()
	reg, _ := json.Marshal(map[string]any{
		"client_name": "Goliash CLI on " + host, "redirect_uris": []string{"http://127.0.0.1/callback"},
		"grant_types": []string{"authorization_code", "refresh_token"}, "token_endpoint_auth_method": "none",
		"application_type": "native",
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, md.Register, strings.NewReader(string(reg)))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return err
	}
	var client struct {
		ClientID string `json:"client_id"`
		Error    string `json:"error_description"`
	}
	_ = json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&client)
	_ = resp.Body.Close()
	if client.ClientID == "" {
		return fmt.Errorf("registering the CLI failed (%d): %s", resp.StatusCode, client.Error)
	}

	verifier, state := randomString(48), randomString(24)
	sum := sha256.Sum256([]byte(verifier))
	authURL := md.Authorize + "?" + url.Values{
		"response_type": {"code"}, "client_id": {client.ClientID}, "redirect_uri": {redirect}, "state": {state},
		"code_challenge": {base64.RawURLEncoding.EncodeToString(sum[:])}, "code_challenge_method": {"S256"},
		"resource": {server + "/mcp"}, "scope": {"goliash:read"},
	}.Encode()

	type result struct {
		code string
		err  error
	}
	done := make(chan result, 1)
	srv := &http.Server{ReadHeaderTimeout: 10 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/callback" {
			http.NotFound(w, r)
			return
		}
		q := r.URL.Query()
		var res result
		switch {
		case q.Get("state") != state:
			res.err = errors.New("the answer does not belong to this sign-in")
		case (md.IssInAnswers || q.Has("iss")) && q.Get("iss") != md.Issuer:
			res.err = errors.New("the answer came from another server")
		case q.Get("error") != "":
			res.err = fmt.Errorf("not allowed: %s", q.Get("error"))
		default:
			res.code = q.Get("code")
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if res.err != nil {
			_, _ = io.WriteString(w, "Goliash CLI: "+res.err.Error()+"\n") //nolint:gosec // plain text, not sniffed
		} else {
			_, _ = io.WriteString(w, "The Goliash CLI is signed in. You can close this tab.\n")
		}
		select {
		case done <- res:
		default:
		}
	})}
	go func() { _ = srv.Serve(ln) }()
	defer func() { _ = srv.Close() }()

	_, _ = fmt.Fprintf(out, "Sign in and allow the CLI in the browser:\n\n  %s\n\n", authURL)
	if !*noBrowser {
		openBrowser(context.WithoutCancel(ctx), authURL)
	}
	var res result
	select {
	case res = <-done:
	case <-time.After(loginTimeout):
		return errors.New("no answer from the browser in five minutes")
	case <-ctx.Done():
		return ctx.Err()
	}
	if res.err != nil {
		return res.err
	}
	tok, err := tokenRequest(ctx, md.Token, url.Values{
		"grant_type": {"authorization_code"}, "code": {res.code}, "redirect_uri": {redirect},
		"code_verifier": {verifier}, "client_id": {client.ClientID}, "resource": {server + "/mcp"},
	})
	if err != nil {
		return fmt.Errorf("getting the tokens failed: %w", err)
	}
	c, err := loadCredentials()
	if err != nil {
		return err
	}
	c.Servers[server] = &credential{
		ClientID: client.ClientID, AccessToken: tok.AccessToken, RefreshToken: tok.RefreshToken,
		ExpiresAt: tok.expiresAt(), TokenURL: md.Token, RevokeURL: md.Revoke,
	}
	c.Default = server
	if err := saveCredentials(c); err != nil {
		return err
	}
	p, _ := credentialsPath()
	_, _ = fmt.Fprintf(out, "Signed in to %s, read only. goliash ask and goliash mcp use it now.\nSaved in %s; goliash logout ends it.\n", server, p)
	return nil
}

func logoutCmd(ctx context.Context, args []string, out io.Writer) error {
	c, err := loadCredentials()
	if err != nil {
		return err
	}
	server := c.Default
	if len(args) > 0 {
		server = args[0]
	}
	server = normServer(server)
	cr := c.Servers[server]
	if cr == nil {
		return fmt.Errorf("not signed in to %q", server)
	}
	if cr.RevokeURL != "" {
		form := url.Values{"token": {cr.RefreshToken}, "client_id": {cr.ClientID}}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, cr.RevokeURL, strings.NewReader(form.Encode()))
		if err == nil {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			if resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req); err == nil {
				_ = resp.Body.Close()
			}
		}
	}
	delete(c.Servers, server)
	if c.Default == server {
		c.Default = ""
	}
	if err := saveCredentials(c); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(out, "Signed out of %s.\n", server)
	return nil
}
