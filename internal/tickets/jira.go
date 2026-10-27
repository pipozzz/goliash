// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package tickets

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// Jira files issues with Jira's REST API v2, which Jira Cloud and Data Center both serve. Cloud signs in with an
// e-mail and an API token, Data Center with a personal access token (no e-mail).
type Jira struct {
	HTTP *http.Client
}

// JiraConfig is a jira channel's settings.
type JiraConfig struct {
	URL       string `json:"url"`        // https://acme.atlassian.net
	Email     string `json:"email"`      // Cloud: the API token's account
	Token     string `json:"token"`      // Cloud API token, or a Data Center personal access token
	Project   string `json:"project"`    // the default project key
	Teams     string `json:"teams"`      // "team-payments=PAY, team-data=DATA": projects by the services' owner
	IssueType string `json:"issue_type"` // default Task
	Labels    string `json:"labels"`     // more labels, comma-separated
}

func (Jira) config(raw json.RawMessage) (JiraConfig, error) {
	var c JiraConfig
	if err := json.Unmarshal(raw, &c); err != nil {
		return c, err
	}
	if c.URL == "" || c.Token == "" || c.Project == "" {
		return c, errors.New("jira: the URL, token and project are required")
	}
	c.URL = strings.TrimSuffix(c.URL, "/")
	if c.IssueType == "" {
		c.IssueType = "Task"
	}
	return c, nil
}

func (c JiraConfig) headers() map[string]string {
	if c.Email != "" {
		return map[string]string{"Authorization": "Basic " + base64.StdEncoding.EncodeToString([]byte(c.Email+":"+c.Token))}
	}
	return map[string]string{"Authorization": "Bearer " + c.Token}
}

var jiraPriorities = []string{"Highest", "High", "Medium", "Low"}

// Create implements Tracker. A project whose screen lacks the priority or due date fields refuses them: the issue is
// filed again without.
func (j Jira) Create(ctx context.Context, raw json.RawMessage, d Draft) (Ref, error) {
	c, err := j.config(raw)
	if err != nil {
		return Ref{}, err
	}
	fields := map[string]any{
		"project":     map[string]string{"key": project(c.Project, ParseTeams(c.Teams), d.Team)},
		"issuetype":   map[string]string{"name": c.IssueType},
		"summary":     d.Title,
		"description": d.Body,
		"labels":      jiraLabels(append(d.Labels, strings.Split(c.Labels, ",")...)),
	}
	optional := map[string]any{"priority": map[string]string{"name": jiraPriorities[min(max(d.Priority, 0), 3)]}}
	if !d.Due.IsZero() {
		optional["duedate"] = d.Due.UTC().Format("2006-01-02")
	}
	for k, v := range optional {
		fields[k] = v
	}
	var out struct {
		ID  string `json:"id"`
		Key string `json:"key"`
	}
	err = call(ctx, j.HTTP, http.MethodPost, c.URL+"/rest/api/2/issue", c.headers(), map[string]any{"fields": fields}, &out)
	var refused *HTTPError
	if errors.As(err, &refused) && refused.Status == http.StatusBadRequest {
		for k := range optional {
			delete(fields, k)
		}
		err = call(ctx, j.HTTP, http.MethodPost, c.URL+"/rest/api/2/issue", c.headers(), map[string]any{"fields": fields}, &out)
	}
	if err != nil {
		return Ref{}, err
	}
	return Ref{ID: out.ID, Key: out.Key, URL: c.URL + "/browse/" + out.Key}, nil
}

// Comment implements Tracker.
func (j Jira) Comment(ctx context.Context, raw json.RawMessage, ref Ref, text string) error {
	c, err := j.config(raw)
	if err != nil {
		return err
	}
	return call(ctx, j.HTTP, http.MethodPost, c.URL+"/rest/api/2/issue/"+url.PathEscape(ref.Key)+"/comment", c.headers(),
		map[string]string{"body": text}, nil)
}

// Close implements Tracker: it comments, then takes the issue through a transition to a done status (one named
// Done, Closed or Resolved first).
func (j Jira) Close(ctx context.Context, raw json.RawMessage, ref Ref, comment string) error {
	c, err := j.config(raw)
	if err != nil {
		return err
	}
	if comment != "" {
		if err := j.Comment(ctx, raw, ref, comment); err != nil {
			return err
		}
	}
	var ts struct {
		Transitions []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
			To   struct {
				Name           string `json:"name"`
				StatusCategory struct {
					Key string `json:"key"`
				} `json:"statusCategory"`
			} `json:"to"`
		} `json:"transitions"`
	}
	base := c.URL + "/rest/api/2/issue/" + url.PathEscape(ref.Key) + "/transitions"
	if err := call(ctx, j.HTTP, http.MethodGet, base, c.headers(), nil, &ts); err != nil {
		return err
	}
	best, rank := "", 0
	for _, t := range ts.Transitions {
		if t.To.StatusCategory.Key != "done" {
			continue
		}
		r := 1
		switch strings.ToLower(t.To.Name) {
		case "done", "closed", "resolved":
			r = 2
		}
		if r > rank {
			best, rank = t.ID, r
		}
	}
	if best == "" {
		return nil // the workflow has no way to done from here; the comment says it is
	}
	return call(ctx, j.HTTP, http.MethodPost, base, c.headers(), map[string]any{"transition": map[string]string{"id": best}}, nil)
}

// Check implements Tracker: the token signs in, and every project exists.
func (j Jira) Check(ctx context.Context, raw json.RawMessage) error {
	c, err := j.config(raw)
	if err != nil {
		return err
	}
	if err := call(ctx, j.HTTP, http.MethodGet, c.URL+"/rest/api/2/myself", c.headers(), nil, nil); err != nil {
		return fmt.Errorf("signing in: %w", err)
	}
	keys := []string{c.Project}
	for _, k := range ParseTeams(c.Teams) {
		keys = append(keys, k)
	}
	for _, k := range keys {
		if err := call(ctx, j.HTTP, http.MethodGet, c.URL+"/rest/api/2/project/"+url.PathEscape(k), c.headers(), nil, nil); err != nil {
			return fmt.Errorf("project %s: %w", k, err)
		}
	}
	return nil
}

// jiraLabels are labels Jira takes: no spaces, each once.
func jiraLabels(in []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, l := range in {
		l = strings.ReplaceAll(strings.TrimSpace(l), " ", "-")
		if l != "" && !seen[l] {
			seen[l] = true
			out = append(out, l)
		}
	}
	return out
}
