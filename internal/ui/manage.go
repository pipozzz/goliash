// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package ui

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/pipozzz/goliash/internal/auth"
	"github.com/pipozzz/goliash/internal/store"
)

// ---- notifications ----

func (s *Server) deleteChannel(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	ctx := r.Context()
	chans, err := s.store.ListChannels(ctx, p.Scope)
	if err != nil {
		return err
	}
	for _, c := range chans {
		if c.ID != r.PathValue("id") {
			continue
		}
		if err := s.store.DeleteChannel(ctx, p.Scope, c.ID); err != nil {
			return err
		}
		s.audit(ctx, p, "channel.delete", "channel", c.Name)
		return back(w, r, "/notifications", "notice", "Channel "+c.Name+" and its rules deleted.")
	}
	return back(w, r, "/notifications", "error", "That channel is already gone.")
}

func (s *Server) pauseRule(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	paused := r.FormValue("paused") == "true"
	err := s.store.SetRulePaused(r.Context(), p.Scope, r.PathValue("id"), paused)
	if errors.Is(err, store.ErrNotFound) {
		return back(w, r, "/notifications", "error", "That rule is gone.")
	}
	if err != nil {
		return err
	}
	if paused {
		s.audit(r.Context(), p, "rule.pause", "rule", r.PathValue("id"))
		return back(w, r, "/notifications", "notice", "Rule paused: it sends nothing until you resume it.")
	}
	s.audit(r.Context(), p, "rule.resume", "rule", r.PathValue("id"))
	return back(w, r, "/notifications", "notice", "Rule resumed.")
}

func (s *Server) deleteRule(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	err := s.store.DeleteRule(r.Context(), p.Scope, r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		return back(w, r, "/notifications", "error", "That rule is already gone.")
	}
	if err != nil {
		return err
	}
	s.audit(r.Context(), p, "rule.delete", "rule", r.PathValue("id"))
	return back(w, r, "/notifications", "notice", "Rule deleted.")
}

// ---- environments ----

func (s *Server) updateEnvironment(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	ctx := r.Context()
	name := strings.TrimSpace(r.FormValue("name"))
	if !serviceName.MatchString(name) {
		return back(w, r, "/agents", "error", "Environment names use letters, digits, dots, dashes and underscores.")
	}
	pos, err := strconv.Atoi(strings.TrimSpace(r.FormValue("position")))
	if err != nil || pos < 0 || pos > 100000 {
		return back(w, r, "/agents", "error", "The order is a whole number, e.g. 10, 20, 30.")
	}
	old := ""
	envs, err := s.store.ListEnvironments(ctx, p.Scope)
	if err != nil {
		return err
	}
	for _, e := range envs {
		if e.ID == r.PathValue("id") {
			old = e.Name
		}
	}
	err = s.store.UpdateEnvironment(ctx, p.Scope, r.PathValue("id"), name, pos)
	switch {
	case errors.Is(err, store.ErrExists):
		return back(w, r, "/agents", "error", "Another environment is named "+name+".")
	case errors.Is(err, store.ErrNotFound):
		return back(w, r, "/agents", "error", "That environment is gone.")
	case err != nil:
		return err
	}
	if s.checker != nil {
		_ = s.checker.EvaluateDrift(ctx, p.Scope) // the order decides "behind previous environment"
	}
	s.hub.Publish(p.Scope.WorkspaceID)
	s.audit(ctx, p, "environment.update", "environment", old, "name", name, "position", strconv.Itoa(pos))
	return back(w, r, "/agents", "notice", "Environment "+name+" saved.")
}

func (s *Server) deleteEnvironment(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	ctx := r.Context()
	envs, err := s.store.ListEnvironments(ctx, p.Scope)
	if err != nil {
		return err
	}
	for _, e := range envs {
		if e.ID != r.PathValue("id") {
			continue
		}
		if err := s.store.DeleteEnvironment(ctx, p.Scope, e.ID); errors.Is(err, store.ErrInUse) {
			return back(w, r, "/agents", "error", "Move or delete the targets of "+e.Name+" first.")
		} else if err != nil {
			return err
		}
		if s.checker != nil {
			_ = s.checker.EvaluateDrift(ctx, p.Scope)
		}
		s.hub.Publish(p.Scope.WorkspaceID)
		s.audit(ctx, p, "environment.delete", "environment", e.Name)
		return back(w, r, "/agents", "notice", "Environment "+e.Name+" deleted.")
	}
	return back(w, r, "/agents", "error", "That environment is already gone.")
}

// ---- targets ----

func targetBack(agentID string) string {
	if agentID == "" {
		return "/agents"
	}
	return "/agents/" + agentID
}

func (s *Server) editTarget(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	ctx := r.Context()
	t, err := s.store.GetTarget(ctx, p.Scope, r.PathValue("id"))
	if err != nil {
		return back(w, r, "/agents", "error", "Unknown target.")
	}
	all, err := s.agentsView(ctx, p)
	if err != nil {
		return err
	}
	var pretty bytes.Buffer
	if json.Indent(&pretty, t.Settings, "", "  ") != nil {
		pretty.Reset()
		pretty.Write(t.Settings)
	}
	v := TargetEditView{
		Base: withFlash(s.base(ctx, p, "agents", t.Name), r), ID: t.ID, Name: t.Name, Platform: t.Platform,
		EnvID: t.EnvironmentID, Envs: all.Envs, Settings: pretty.String(), Poll: t.PollIntervalSeconds, AgentID: t.AgentID,
	}
	for _, a := range all.Moves {
		if a.ID == t.AgentID {
			v.Agent = a.Name
		}
	}
	return render(w, r, TargetPage(v))
}

func (s *Server) updateTarget(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	ctx := r.Context()
	t, err := s.store.GetTarget(ctx, p.Scope, r.PathValue("id"))
	if err != nil {
		return back(w, r, "/agents", "error", "Unknown target.")
	}
	self := "/targets/" + t.ID
	settings := strings.TrimSpace(r.FormValue("settings"))
	if settings == "" {
		settings = "{}"
	}
	var probe map[string]json.RawMessage
	if err := json.Unmarshal([]byte(settings), &probe); err != nil {
		return back(w, r, self, "error", "Settings must be a JSON object.")
	}
	var compact bytes.Buffer
	_ = json.Compact(&compact, []byte(settings))
	poll, err := strconv.Atoi(strings.TrimSpace(r.FormValue("poll")))
	if err != nil || poll < 30 || poll > 86400 {
		return back(w, r, self, "error", "Poll every 30 to 86400 seconds.")
	}
	envName := ""
	envs, err := s.store.ListEnvironments(ctx, p.Scope)
	if err != nil {
		return err
	}
	for _, e := range envs {
		if e.ID == r.FormValue("environment") {
			envName = e.Name
		}
	}
	if envName == "" {
		return back(w, r, self, "error", "Unknown environment.")
	}
	if err := s.store.UpdateTarget(ctx, p.Scope, t.ID, r.FormValue("environment"), compact.Bytes(), poll); err != nil {
		return err
	}
	if t.EnvironmentID != r.FormValue("environment") && s.checker != nil {
		_ = s.checker.EvaluateDrift(ctx, p.Scope)
	}
	s.hub.Publish(p.Scope.WorkspaceID)
	s.audit(ctx, p, "target.update", "target", t.Name, "environment", envName, "poll", strconv.Itoa(poll))
	return back(w, r, targetBack(t.AgentID), "notice", "Target "+t.Name+" saved. Its collector picks the change up with the next configuration poll.")
}

// ---- mapping rules and acknowledgements ----

func (s *Server) deleteMappingRule(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	ctx := r.Context()
	err := s.store.DeleteMappingRule(ctx, p.Scope, r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		return back(w, r, "/inbox", "error", "That rule is already gone.")
	}
	if err != nil {
		return err
	}
	s.audit(ctx, p, "mapping_rule.delete", "rule", r.PathValue("id"))
	return back(w, r, "/inbox", "notice", "Rule deleted. Workloads it matched are mapped again with the next snapshot.")
}

func (s *Server) deleteAck(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	ctx := r.Context()
	name := r.PathValue("name")
	err := s.store.DeleteAck(ctx, p.Scope, r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		return back(w, r, "/services/"+name, "error", "That acknowledgement is already gone.")
	}
	if err != nil {
		return err
	}
	s.audit(ctx, p, "ack.delete", "service", name)
	return back(w, r, "/services/"+name, "notice", "Acknowledgement removed: its notifications come again.")
}
