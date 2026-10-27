// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package tickets

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
)

// GitHub files issues with GitHub's REST API (github.com, or GitHub Enterprise Server at its API URL).
type GitHub struct {
	HTTP *http.Client
}

// GitHubConfig is a github_issues channel's settings.
type GitHubConfig struct {
	API    string `json:"url"`   // the API; empty for https://api.github.com, https://github.example.com/api/v3 for a server
	Token  string `json:"token"` // a token that may write issues in the repositories
	Repo   string `json:"repo"`  // the default repository, owner/name
	Teams  string `json:"teams"` // "team-payments=acme/payments": repositories by the services' owner
	Labels string `json:"labels"`
}

func (GitHub) config(raw json.RawMessage) (GitHubConfig, error) {
	var c GitHubConfig
	if err := json.Unmarshal(raw, &c); err != nil {
		return c, err
	}
	if c.Token == "" || strings.Count(c.Repo, "/") != 1 {
		return c, errors.New("github: a token and a repository (owner/name) are required")
	}
	if c.API == "" {
		c.API = "https://api.github.com"
	}
	c.API = strings.TrimSuffix(c.API, "/")
	return c, nil
}

func (c GitHubConfig) headers() map[string]string {
	return map[string]string{"Authorization": "Bearer " + c.Token, "Accept": "application/vnd.github+json", "X-GitHub-Api-Version": "2022-11-28"}
}

// Create implements Tracker. GitHub issues have no priority or due date: the priority is a label, the deadline is
// in the body.
func (g GitHub) Create(ctx context.Context, raw json.RawMessage, d Draft) (Ref, error) {
	c, err := g.config(raw)
	if err != nil {
		return Ref{}, err
	}
	repo := project(c.Repo, ParseTeams(c.Teams), d.Team)
	labels := jiraLabels(append(append(d.Labels, "priority: "+strings.ToLower(jiraPriorities[min(max(d.Priority, 0), 3)])), strings.Split(c.Labels, ",")...))
	for i, l := range labels {
		labels[i] = strings.ReplaceAll(l, "priority:-", "priority: ")
	}
	var out struct {
		Number  int    `json:"number"`
		HTMLURL string `json:"html_url"`
	}
	in := map[string]any{"title": d.Title, "body": d.Body, "labels": labels}
	if err := call(ctx, g.HTTP, http.MethodPost, c.API+"/repos/"+repo+"/issues", c.headers(), in, &out); err != nil {
		return Ref{}, err
	}
	n := strconv.Itoa(out.Number)
	return Ref{ID: n, Key: repo + "#" + n, URL: out.HTMLURL}, nil
}

// issue is the API path of a filed issue, from its key (owner/name#123).
func (c GitHubConfig) issue(ref Ref) (string, error) {
	repo, n, ok := strings.Cut(ref.Key, "#")
	if !ok {
		return "", fmt.Errorf("github: %q is not owner/name#number", ref.Key)
	}
	return c.API + "/repos/" + repo + "/issues/" + n, nil
}

// Comment implements Tracker.
func (g GitHub) Comment(ctx context.Context, raw json.RawMessage, ref Ref, text string) error {
	c, err := g.config(raw)
	if err != nil {
		return err
	}
	path, err := c.issue(ref)
	if err != nil {
		return err
	}
	return call(ctx, g.HTTP, http.MethodPost, path+"/comments", c.headers(), map[string]string{"body": text}, nil)
}

// Close implements Tracker.
func (g GitHub) Close(ctx context.Context, raw json.RawMessage, ref Ref, comment string) error {
	c, err := g.config(raw)
	if err != nil {
		return err
	}
	path, err := c.issue(ref)
	if err != nil {
		return err
	}
	if comment != "" {
		if err := call(ctx, g.HTTP, http.MethodPost, path+"/comments", c.headers(), map[string]string{"body": comment}, nil); err != nil {
			return err
		}
	}
	return call(ctx, g.HTTP, http.MethodPatch, path, c.headers(), map[string]string{"state": "closed", "state_reason": "completed"}, nil)
}

// Check implements Tracker: every repository exists and has issues on.
func (g GitHub) Check(ctx context.Context, raw json.RawMessage) error {
	c, err := g.config(raw)
	if err != nil {
		return err
	}
	repos := []string{c.Repo}
	for _, r := range ParseTeams(c.Teams) {
		repos = append(repos, r)
	}
	for _, r := range repos {
		var out struct {
			HasIssues bool `json:"has_issues"`
		}
		if err := call(ctx, g.HTTP, http.MethodGet, c.API+"/repos/"+r, c.headers(), nil, &out); err != nil {
			return fmt.Errorf("repository %s: %w", r, err)
		}
		if !out.HasIssues {
			return fmt.Errorf("repository %s has issues turned off", r)
		}
	}
	return nil
}
