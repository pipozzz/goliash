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

// Shortcut files stories with Shortcut's REST API v3.
type Shortcut struct {
	HTTP *http.Client
	Base string // the API; empty for https://api.app.shortcut.com/api/v3
}

// ShortcutConfig is a shortcut channel's settings.
type ShortcutConfig struct {
	Token     string `json:"token"`
	Team      string `json:"team"`       // the default team, by mention name or ID
	Teams     string `json:"teams"`      // "team-payments=payments": teams by the services' owner
	StoryType string `json:"story_type"` // chore (default), bug or feature
	Labels    string `json:"labels"`
}

func (s Shortcut) config(raw json.RawMessage) (ShortcutConfig, string, error) {
	var c ShortcutConfig
	if err := json.Unmarshal(raw, &c); err != nil {
		return c, "", err
	}
	if c.Token == "" {
		return c, "", errors.New("shortcut: the API token is required")
	}
	if c.StoryType == "" {
		c.StoryType = "chore"
	}
	base := s.Base
	if base == "" {
		base = "https://api.app.shortcut.com/api/v3"
	}
	return c, strings.TrimSuffix(base, "/"), nil
}

func (c ShortcutConfig) headers() map[string]string {
	return map[string]string{"Shortcut-Token": c.Token}
}

// groupID finds a team by mention name (or takes an ID as it is).
func (s Shortcut) groupID(ctx context.Context, c ShortcutConfig, base, team string) (string, error) {
	if team == "" || strings.Count(team, "-") == 4 { // empty, or a UUID already
		return team, nil
	}
	var groups []struct {
		ID          string `json:"id"`
		MentionName string `json:"mention_name"`
		Name        string `json:"name"`
	}
	if err := call(ctx, s.HTTP, http.MethodGet, base+"/groups", c.headers(), nil, &groups); err != nil {
		return "", err
	}
	for _, g := range groups {
		if strings.EqualFold(g.MentionName, strings.TrimPrefix(team, "@")) || strings.EqualFold(g.Name, team) {
			return g.ID, nil
		}
	}
	return "", fmt.Errorf("shortcut: no team %q", team)
}

// Create implements Tracker. Priority goes in as a label (Shortcut's priority field is a custom one).
func (s Shortcut) Create(ctx context.Context, raw json.RawMessage, d Draft) (Ref, error) {
	c, base, err := s.config(raw)
	if err != nil {
		return Ref{}, err
	}
	group, err := s.groupID(ctx, c, base, project(c.Team, ParseTeams(c.Teams), d.Team))
	if err != nil {
		return Ref{}, err
	}
	var labels []map[string]string
	for _, l := range jiraLabels(append(append(d.Labels, "priority-"+strings.ToLower(jiraPriorities[min(max(d.Priority, 0), 3)])), strings.Split(c.Labels, ",")...)) {
		labels = append(labels, map[string]string{"name": l})
	}
	in := map[string]any{"name": d.Title, "description": d.Body, "story_type": c.StoryType, "labels": labels}
	if group != "" {
		in["group_id"] = group
	}
	if !d.Due.IsZero() {
		in["deadline"] = d.Due.UTC().Format("2006-01-02T15:04:05Z")
	}
	var out struct {
		ID     int    `json:"id"`
		AppURL string `json:"app_url"`
	}
	if err := call(ctx, s.HTTP, http.MethodPost, base+"/stories", c.headers(), in, &out); err != nil {
		return Ref{}, err
	}
	return Ref{ID: strconv.Itoa(out.ID), Key: "sc-" + strconv.Itoa(out.ID), URL: out.AppURL}, nil
}

// Comment implements Tracker.
func (s Shortcut) Comment(ctx context.Context, raw json.RawMessage, ref Ref, text string) error {
	c, base, err := s.config(raw)
	if err != nil {
		return err
	}
	return call(ctx, s.HTTP, http.MethodPost, base+"/stories/"+ref.ID+"/comments", c.headers(), map[string]string{"text": text}, nil)
}

// Close implements Tracker: it comments, then moves the story to its workflow's first done state.
func (s Shortcut) Close(ctx context.Context, raw json.RawMessage, ref Ref, comment string) error {
	c, base, err := s.config(raw)
	if err != nil {
		return err
	}
	if comment != "" {
		if err := s.Comment(ctx, raw, ref, comment); err != nil {
			return err
		}
	}
	var story struct {
		WorkflowID int `json:"workflow_id"`
	}
	if err := call(ctx, s.HTTP, http.MethodGet, base+"/stories/"+ref.ID, c.headers(), nil, &story); err != nil {
		return err
	}
	var wf struct {
		States []struct {
			ID   int    `json:"id"`
			Type string `json:"type"`
		} `json:"states"`
	}
	if err := call(ctx, s.HTTP, http.MethodGet, base+"/workflows/"+strconv.Itoa(story.WorkflowID), c.headers(), nil, &wf); err != nil {
		return err
	}
	for _, st := range wf.States {
		if st.Type == "done" {
			return call(ctx, s.HTTP, http.MethodPut, base+"/stories/"+ref.ID, c.headers(), map[string]int{"workflow_state_id": st.ID}, nil)
		}
	}
	return nil
}

// Check implements Tracker: the token works, and every team exists.
func (s Shortcut) Check(ctx context.Context, raw json.RawMessage) error {
	c, base, err := s.config(raw)
	if err != nil {
		return err
	}
	if err := call(ctx, s.HTTP, http.MethodGet, base+"/member", c.headers(), nil, nil); err != nil {
		return fmt.Errorf("signing in: %w", err)
	}
	teams := []string{c.Team}
	for _, t := range ParseTeams(c.Teams) {
		teams = append(teams, t)
	}
	for _, t := range teams {
		if _, err := s.groupID(ctx, c, base, t); err != nil {
			return err
		}
	}
	return nil
}
