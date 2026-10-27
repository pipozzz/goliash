// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

// Package tickets files what Goliash finds as tickets in an issue tracker (Jira, …): one per upgrade of a service in
// an environment, commented on when more comes up, and closed when the upgrade is deployed.
package tickets

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"
)

// Draft is a ticket to file.
type Draft struct {
	Title    string
	Body     string    // plain text, paragraphs separated by blank lines
	Priority int       // 0 most urgent … 3 least
	Due      time.Time // the remediation deadline; zero without one
	Labels   []string
	Team     string // the service's owner, to pick the tracker's project or team
}

// Ref is a filed ticket.
type Ref struct {
	ID, Key, URL string
}

// Tracker files, comments on and closes tickets in one kind of issue tracker. cfg is the channel's settings.
type Tracker interface {
	Create(ctx context.Context, cfg json.RawMessage, d Draft) (Ref, error)
	Comment(ctx context.Context, cfg json.RawMessage, ref Ref, text string) error
	Close(ctx context.Context, cfg json.RawMessage, ref Ref, comment string) error
	// Check signs in and finds the projects, filing nothing.
	Check(ctx context.Context, cfg json.RawMessage) error
}

// Kinds are the channel types that file tickets.
var Kinds = []string{"jira", "shortcut", "github_issues", "gitlab_issues", "linear"}

// Is reports whether a channel type files tickets.
func Is(kind string) bool { return slices.Contains(Kinds, kind) }

// For returns the tracker of a kind, or nil.
func For(kind string, hc *http.Client) Tracker {
	switch kind {
	case "jira":
		return Jira{HTTP: hc}
	case "shortcut":
		return Shortcut{HTTP: hc}
	case "github_issues":
		return GitHub{HTTP: hc}
	case "gitlab_issues":
		return GitLab{HTTP: hc}
	case "linear":
		return Linear{HTTP: hc}
	}
	return nil
}

// project picks the project (or team) of a ticket: the one mapped to its team, else the default.
func project(def string, byTeam map[string]string, team string) string {
	if p := byTeam[team]; p != "" {
		return p
	}
	return def
}

// ParseTeams reads "team-payments=PAY, team-data=DATA".
func ParseTeams(s string) map[string]string {
	out := map[string]string{}
	for _, pair := range strings.Split(s, ",") {
		if team, p, ok := strings.Cut(pair, "="); ok && strings.TrimSpace(team) != "" && strings.TrimSpace(p) != "" {
			out[strings.TrimSpace(team)] = strings.TrimSpace(p)
		}
	}
	return out
}

// call sends a JSON request and decodes a JSON answer into out (when not nil).
func call(ctx context.Context, hc *http.Client, method, url string, headers map[string]string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("User-Agent", "goliash")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 400))
		return &HTTPError{Status: resp.StatusCode, Body: strings.TrimSpace(string(b))}
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// HTTPError is a tracker's refusal.
type HTTPError struct {
	Status int
	Body   string
}

func (e *HTTPError) Error() string { return fmt.Sprintf("answered %d: %s", e.Status, e.Body) }
