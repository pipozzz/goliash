// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package ui

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/pipozzz/goliash/internal/auth"
	"github.com/pipozzz/goliash/internal/store"
)

// CodeEnvView is the environment picker of an enrollment code: on the page that shows its command, and on the
// Agents page later.
type CodeEnvView struct {
	CodeID string
	Env    string // its environment's ID
	Envs   []store.Environment
	Moved  string // what the last change did, said once
}

// setCodeEnv moves an enrollment code to another environment: what its agents find from now on lands there, and the
// targets they added already move with it. From the command page it answers with the picker alone (htmx), so the
// command, shown only once, stays on screen.
func (s *Server) setCodeEnv(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	ctx := r.Context()
	id, envID := r.PathValue("id"), r.FormValue("environment")
	envs, err := s.store.ListEnvironments(ctx, p.Scope)
	if err != nil {
		return err
	}
	var env store.Environment
	for _, e := range envs {
		if e.ID == envID {
			env = e
		}
	}
	if env.ID == "" {
		return back(w, r, "/agents", "error", "Pick an environment.")
	}
	if err := s.store.SetEnrollmentCodeEnvironment(ctx, p.Scope, id, env.ID); errors.Is(err, store.ErrNotFound) {
		return back(w, r, "/agents", "error", "Unknown code.")
	} else if err != nil {
		return err
	}
	agents, err := s.store.ListCodeAgents(ctx, p.Scope, id)
	if err != nil {
		return err
	}
	moved := 0
	for _, a := range agents {
		ts, err := s.store.ListAgentTargets(ctx, p.Scope, a.ID)
		if err != nil {
			return err
		}
		for _, t := range ts {
			if t.EnvironmentID == env.ID {
				continue
			}
			if err := s.store.UpdateTarget(ctx, p.Scope, t.ID, env.ID, t.Settings, t.PollIntervalSeconds); err != nil {
				return err
			}
			moved++
		}
	}
	s.audit(ctx, p, "enroll.environment", "code", id, "environment", env.Name, "targets_moved", strconv.Itoa(moved))
	if s.checker != nil && moved > 0 {
		_ = s.checker.EvaluateDrift(ctx, p.Scope)
	}
	s.hub.Publish(p.Scope.WorkspaceID)
	msg := "New agents land in " + env.Name + "."
	if moved > 0 {
		msg = plural(moved, "target", "targets") + " moved to " + env.Name + "; new ones land there too."
	}
	if r.Header.Get("HX-Request") == "true" && r.FormValue("inline") == "1" {
		return render(w, r, codeEnvPicker(CodeEnvView{CodeID: id, Env: env.ID, Envs: envs, Moved: msg}, true))
	}
	return back(w, r, "/agents", "notice", msg)
}
