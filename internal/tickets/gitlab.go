// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package tickets

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// GitLab files issues with GitLab's REST API v4 (gitlab.com or a self-managed instance).
type GitLab struct {
	HTTP *http.Client
}

// GitLabConfig is a gitlab_issues channel's settings.
type GitLabConfig struct {
	URL     string `json:"url"`     // the instance; empty for https://gitlab.com
	Token   string `json:"token"`   // a token with the api scope
	Project string `json:"project"` // the default project, group/name
	Teams   string `json:"teams"`   // "team-payments=acme/payments": projects by the services' owner
	Labels  string `json:"labels"`
}

func (GitLab) config(raw json.RawMessage) (GitLabConfig, error) {
	var c GitLabConfig
	if err := json.Unmarshal(raw, &c); err != nil {
		return c, err
	}
	if c.Token == "" || !strings.Contains(c.Project, "/") {
		return c, errors.New("gitlab: a token and a project (group/name) are required")
	}
	if c.URL == "" {
		c.URL = "https://gitlab.com"
	}
	c.URL = strings.TrimSuffix(c.URL, "/")
	return c, nil
}

func (c GitLabConfig) headers() map[string]string { return map[string]string{"PRIVATE-TOKEN": c.Token} }

func (c GitLabConfig) api(project string) string {
	return c.URL + "/api/v4/projects/" + url.PathEscape(project)
}

// Create implements Tracker. The priority is a label; the remediation deadline is the due date.
func (g GitLab) Create(ctx context.Context, raw json.RawMessage, d Draft) (Ref, error) {
	c, err := g.config(raw)
	if err != nil {
		return Ref{}, err
	}
	proj := project(c.Project, ParseTeams(c.Teams), d.Team)
	labels := jiraLabels(append(append(d.Labels, "priority::"+strings.ToLower(jiraPriorities[min(max(d.Priority, 0), 3)])), strings.Split(c.Labels, ",")...))
	in := map[string]any{"title": d.Title, "description": d.Body, "labels": strings.Join(labels, ",")}
	if !d.Due.IsZero() {
		in["due_date"] = d.Due.UTC().Format("2006-01-02")
	}
	var out struct {
		IID    int    `json:"iid"`
		WebURL string `json:"web_url"`
	}
	if err := call(ctx, g.HTTP, http.MethodPost, c.api(proj)+"/issues", c.headers(), in, &out); err != nil {
		return Ref{}, err
	}
	n := strconv.Itoa(out.IID)
	return Ref{ID: n, Key: proj + "#" + n, URL: out.WebURL}, nil
}

func (c GitLabConfig) issue(ref Ref) (string, error) {
	proj, n, ok := strings.Cut(ref.Key, "#")
	if !ok {
		return "", fmt.Errorf("gitlab: %q is not group/name#iid", ref.Key)
	}
	return c.api(proj) + "/issues/" + n, nil
}

// Comment implements Tracker.
func (g GitLab) Comment(ctx context.Context, raw json.RawMessage, ref Ref, text string) error {
	c, err := g.config(raw)
	if err != nil {
		return err
	}
	path, err := c.issue(ref)
	if err != nil {
		return err
	}
	return call(ctx, g.HTTP, http.MethodPost, path+"/notes", c.headers(), map[string]string{"body": text}, nil)
}

// Close implements Tracker.
func (g GitLab) Close(ctx context.Context, raw json.RawMessage, ref Ref, comment string) error {
	c, err := g.config(raw)
	if err != nil {
		return err
	}
	path, err := c.issue(ref)
	if err != nil {
		return err
	}
	if comment != "" {
		if err := call(ctx, g.HTTP, http.MethodPost, path+"/notes", c.headers(), map[string]string{"body": comment}, nil); err != nil {
			return err
		}
	}
	return call(ctx, g.HTTP, http.MethodPut, path, c.headers(), map[string]string{"state_event": "close"}, nil)
}

// Check implements Tracker: every project exists for the token.
func (g GitLab) Check(ctx context.Context, raw json.RawMessage) error {
	c, err := g.config(raw)
	if err != nil {
		return err
	}
	projects := []string{c.Project}
	for _, p := range ParseTeams(c.Teams) {
		projects = append(projects, p)
	}
	for _, p := range projects {
		if err := call(ctx, g.HTTP, http.MethodGet, c.api(p), c.headers(), nil, nil); err != nil {
			return fmt.Errorf("project %s: %w", p, err)
		}
	}
	return nil
}
