// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package ui

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/pipozzz/goliash/internal/auth"
	"github.com/pipozzz/goliash/internal/mapping"
	"github.com/pipozzz/goliash/internal/store"
	"github.com/pipozzz/goliash/internal/versions"
)

// cleanupAge is how long something has to be silent, or unused, before the clean-up page offers to delete it.
const cleanupAge = 7 * 24 * time.Hour

// CleanupView lists what a workspace holds for nothing: agents gone quiet, targets nothing reports for,
// environments without targets, services that run nowhere, and mapping rules that match nothing.
type CleanupView struct {
	Base
	Agents   []CleanupItem
	Targets  []CleanupItem
	Envs     []CleanupItem
	Services []CleanupItem
	Rules    []CleanupItem
}

// CleanupItem is one thing to delete, and why it is offered.
type CleanupItem struct {
	ID, Name, Why string
	Since         time.Time // when it was last heard of or used; zero: never
}

// Total is how many things the page offers to delete.
func (v CleanupView) Total() int {
	return len(v.Agents) + len(v.Targets) + len(v.Envs) + len(v.Services) + len(v.Rules)
}

func (s *Server) cleanup(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	v, err := s.cleanupView(r.Context(), p, time.Now())
	if err != nil {
		return err
	}
	v.Base = withFlash(s.base(r.Context(), p, "agents", "Clean up"), r)
	return render(w, r, CleanupPage(v))
}

func (s *Server) cleanupView(ctx context.Context, p auth.Principal, now time.Time) (CleanupView, error) {
	var v CleanupView
	agents, err := s.store.ListAgents(ctx, p.Scope)
	if err != nil {
		return v, err
	}
	gone := map[string]bool{}
	for _, a := range agents {
		last := a.LastSeenAt
		if last.IsZero() {
			last = a.CreatedAt
		}
		if now.Sub(last) > cleanupAge {
			gone[a.ID] = true
			why := "not heard from since " + last.Format("2 Jan 2006")
			if a.LastSeenAt.IsZero() {
				why = "never connected"
			}
			v.Agents = append(v.Agents, CleanupItem{ID: a.ID, Name: a.Name, Why: why, Since: a.LastSeenAt})
		}
	}
	targets, err := s.store.ListTargets(ctx, p.Scope)
	if err != nil {
		return v, err
	}
	perEnv := map[string]int{}
	for _, t := range targets {
		perEnv[t.EnvironmentID]++
		if gone[t.AgentID] || versions.Manual(t.Platform) || versions.Declared(t.Platform) {
			continue // goes with its agent; or entered by hand, or files in Git: nothing reports for those
		}
		last := t.LastSnapshotAt
		if last.IsZero() {
			last = t.CreatedAt
		}
		if now.Sub(last) > cleanupAge {
			why := "no report since " + last.Format("2 Jan 2006")
			if t.LastSnapshotAt.IsZero() {
				why = "never reported"
			}
			v.Targets = append(v.Targets, CleanupItem{ID: t.ID, Name: t.Name + " (" + t.Platform + ")", Why: why, Since: t.LastSnapshotAt})
		}
	}
	envs, err := s.store.ListEnvironments(ctx, p.Scope)
	if err != nil {
		return v, err
	}
	for _, e := range envs {
		if perEnv[e.ID] == 0 {
			v.Envs = append(v.Envs, CleanupItem{ID: e.ID, Name: e.Name, Why: "no targets"})
		}
	}
	o, err := versions.LoadOverview(ctx, s.store, p.Scope)
	if err != nil {
		return v, err
	}
	runs := map[string]bool{}
	for _, row := range o.Matrix.Rows {
		cells := row.Cells
		for _, part := range row.Parts {
			cells = append(cells, part.Cells...)
		}
		for _, c := range cells {
			if len(c.Versions) > 0 {
				runs[row.Service.ID] = true
			}
		}
	}
	services, err := s.store.ListServices(ctx, p.Scope)
	if err != nil {
		return v, err
	}
	exists := map[string]bool{}
	for _, svc := range services {
		exists[svc.ID] = true
		if !runs[svc.ID] && now.Sub(svc.CreatedAt) > 24*time.Hour {
			v.Services = append(v.Services, CleanupItem{ID: svc.ID, Name: svc.Name, Why: "runs nowhere"})
		}
	}
	rules, err := s.store.ListMappingRules(ctx, p.Scope)
	if err != nil {
		return v, err
	}
	for _, rule := range rules {
		if why := inertRule(rule, exists); why != "" {
			v.Rules = append(v.Rules, CleanupItem{ID: rule.ID, Name: rule.MatchType + " " + rule.Pattern, Why: why})
		}
	}
	return v, nil
}

// inertRule says why a mapping rule does nothing: its service is gone, or it names a shared image (a Lambda runtime,
// a database engine) that rules no longer match. Empty for a rule that works.
func inertRule(r store.MappingRule, services map[string]bool) string {
	if r.ServiceID != "" && !services[r.ServiceID] {
		return "its service is gone"
	}
	if r.MatchType == "image_repo" {
		repo := strings.TrimSuffix(strings.TrimPrefix(r.Pattern, "^"), "$")
		repo = strings.ReplaceAll(repo, `\`, "")
		if mapping.SharedImage(repo) {
			return "a Lambda runtime or a database engine: matched by name instead"
		}
	}
	return ""
}

// cleanupDelete deletes what was ticked, if the page still offers it: something heard from meanwhile stays.
func (s *Server) cleanupDelete(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	ctx := r.Context()
	if err := r.ParseForm(); err != nil {
		return err
	}
	v, err := s.cleanupView(ctx, p, time.Now())
	if err != nil {
		return err
	}
	what := r.PostForm.Get("what")
	offered := map[string][]CleanupItem{"agents": v.Agents, "targets": v.Targets, "envs": v.Envs, "services": v.Services, "rules": v.Rules}[what]
	ticked := r.PostForm["id"]
	deleted, kept := 0, 0
	for _, it := range offered {
		if !slices.Contains(ticked, it.ID) {
			continue
		}
		if err := s.cleanupOne(ctx, p, what, it); errors.Is(err, store.ErrInUse) || errors.Is(err, store.ErrNotFound) {
			kept++
			continue
		} else if err != nil {
			return err
		}
		deleted++
	}
	if deleted > 0 && s.checker != nil {
		_ = s.checker.EvaluateDrift(ctx, p.Scope)
	}
	s.hub.Publish(p.Scope.WorkspaceID)
	msg := "Deleted " + strconv.Itoa(deleted) + "."
	if kept > 0 {
		msg += " " + strconv.Itoa(kept) + " could not go: something still uses them."
	}
	return back(w, r, "/cleanup", "notice", msg)
}

func (s *Server) cleanupOne(ctx context.Context, p auth.Principal, what string, it CleanupItem) error {
	switch what {
	case "agents":
		ts, err := s.store.ListAgentTargets(ctx, p.Scope, it.ID)
		if err != nil {
			return err
		}
		for _, t := range ts {
			if err := s.store.DeleteTarget(ctx, p.Scope, t.ID); err != nil {
				return err
			}
			s.audit(ctx, p, "target.delete", "target", t.Name, "agent", it.Name, "via", "clean up")
		}
		if err := s.store.DeleteAgent(ctx, p.Scope, it.ID); err != nil {
			return err
		}
		s.audit(ctx, p, "agent.delete", "agent", it.Name, "via", "clean up")
	case "targets":
		if err := s.store.DeleteTarget(ctx, p.Scope, it.ID); err != nil {
			return err
		}
		s.audit(ctx, p, "target.delete", "target", it.Name, "via", "clean up")
	case "envs":
		if err := s.store.DeleteEnvironment(ctx, p.Scope, it.ID); err != nil {
			return err
		}
		s.audit(ctx, p, "environment.delete", "environment", it.Name, "via", "clean up")
	case "services":
		if err := s.store.DeleteService(ctx, p.Scope, it.ID); err != nil {
			return err
		}
		s.audit(ctx, p, "service.delete", "service", it.Name, "via", "clean up")
	case "rules":
		if err := s.store.DeleteMappingRule(ctx, p.Scope, it.ID); err != nil {
			return err
		}
		s.audit(ctx, p, "mapping.delete", "rule", it.Name, "via", "clean up")
	}
	return nil
}
