// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package tickets

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

// Linear files issues with Linear's GraphQL API.
type Linear struct {
	HTTP *http.Client
	API  string // empty for https://api.linear.app/graphql
}

// LinearConfig is a linear channel's settings.
type LinearConfig struct {
	Key   string `json:"token"` // a personal API key
	Team  string `json:"team"`  // the default team, by key (ENG) or name
	Teams string `json:"teams"` // "team-payments=PAY": teams by the services' owner
}

func (l Linear) config(raw json.RawMessage) (LinearConfig, string, error) {
	var c LinearConfig
	if err := json.Unmarshal(raw, &c); err != nil {
		return c, "", err
	}
	if c.Key == "" || c.Team == "" {
		return c, "", errors.New("linear: an API key and a team are required")
	}
	api := l.API
	if api == "" {
		api = "https://api.linear.app/graphql"
	}
	return c, api, nil
}

// query runs a GraphQL query and decodes its data into out.
func (l Linear) query(ctx context.Context, c LinearConfig, api, q string, vars map[string]any, out any) error {
	var resp struct {
		Data   json.RawMessage `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := call(ctx, l.HTTP, http.MethodPost, api, map[string]string{"Authorization": c.Key},
		map[string]any{"query": q, "variables": vars}, &resp); err != nil {
		return err
	}
	if len(resp.Errors) > 0 {
		return fmt.Errorf("linear: %s", resp.Errors[0].Message)
	}
	return json.Unmarshal(resp.Data, out)
}

// teamID finds a team by key or name.
func (l Linear) teamID(ctx context.Context, c LinearConfig, api, team string) (string, error) {
	var out struct {
		Teams struct {
			Nodes []struct{ ID, Key, Name string } `json:"nodes"`
		} `json:"teams"`
	}
	if err := l.query(ctx, c, api, `query { teams(first: 250) { nodes { id key name } } }`, nil, &out); err != nil {
		return "", err
	}
	for _, t := range out.Teams.Nodes {
		if strings.EqualFold(t.Key, team) || strings.EqualFold(t.Name, team) || t.ID == team {
			return t.ID, nil
		}
	}
	return "", fmt.Errorf("linear: no team %q", team)
}

// Create implements Tracker. Linear's priorities run 1 (urgent) to 4 (low).
func (l Linear) Create(ctx context.Context, raw json.RawMessage, d Draft) (Ref, error) {
	c, api, err := l.config(raw)
	if err != nil {
		return Ref{}, err
	}
	team, err := l.teamID(ctx, c, api, project(c.Team, ParseTeams(c.Teams), d.Team))
	if err != nil {
		return Ref{}, err
	}
	input := map[string]any{"teamId": team, "title": d.Title, "description": d.Body, "priority": min(max(d.Priority, 0), 3) + 1}
	if !d.Due.IsZero() {
		input["dueDate"] = d.Due.UTC().Format("2006-01-02")
	}
	var out struct {
		IssueCreate struct {
			Success bool                                 `json:"success"`
			Issue   struct{ ID, Identifier, URL string } `json:"issue"`
		} `json:"issueCreate"`
	}
	if err := l.query(ctx, c, api, `mutation($input: IssueCreateInput!) { issueCreate(input: $input) { success issue { id identifier url } } }`,
		map[string]any{"input": input}, &out); err != nil {
		return Ref{}, err
	}
	if !out.IssueCreate.Success {
		return Ref{}, errors.New("linear: the issue was not created")
	}
	i := out.IssueCreate.Issue
	return Ref{ID: i.ID, Key: i.Identifier, URL: i.URL}, nil
}

// Comment implements Tracker.
func (l Linear) Comment(ctx context.Context, raw json.RawMessage, ref Ref, text string) error {
	c, api, err := l.config(raw)
	if err != nil {
		return err
	}
	var out json.RawMessage
	return l.query(ctx, c, api, `mutation($input: CommentCreateInput!) { commentCreate(input: $input) { success } }`,
		map[string]any{"input": map[string]string{"issueId": ref.ID, "body": text}}, &out)
}

// Close implements Tracker: it comments, then moves the issue to its team's first completed state.
func (l Linear) Close(ctx context.Context, raw json.RawMessage, ref Ref, comment string) error {
	c, api, err := l.config(raw)
	if err != nil {
		return err
	}
	if comment != "" {
		if err := l.Comment(ctx, raw, ref, comment); err != nil {
			return err
		}
	}
	var out struct {
		Issue struct {
			Team struct {
				States struct {
					Nodes []struct{ ID, Type string } `json:"nodes"`
				} `json:"states"`
			} `json:"team"`
		} `json:"issue"`
	}
	if err := l.query(ctx, c, api, `query($id: String!) { issue(id: $id) { team { states { nodes { id type } } } } }`,
		map[string]any{"id": ref.ID}, &out); err != nil {
		return err
	}
	for _, s := range out.Issue.Team.States.Nodes {
		if s.Type == "completed" {
			var done json.RawMessage
			return l.query(ctx, c, api, `mutation($id: String!, $input: IssueUpdateInput!) { issueUpdate(id: $id, input: $input) { success } }`,
				map[string]any{"id": ref.ID, "input": map[string]string{"stateId": s.ID}}, &done)
		}
	}
	return nil
}

// Check implements Tracker: the key works, and every team exists.
func (l Linear) Check(ctx context.Context, raw json.RawMessage) error {
	c, api, err := l.config(raw)
	if err != nil {
		return err
	}
	teams := []string{c.Team}
	for _, t := range ParseTeams(c.Teams) {
		teams = append(teams, t)
	}
	for _, t := range teams {
		if _, err := l.teamID(ctx, c, api, t); err != nil {
			return err
		}
	}
	return nil
}
