// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package ui

import (
	"errors"
	"net/http"
	"strings"

	"github.com/pipozzz/goliash/internal/auth"
	"github.com/pipozzz/goliash/internal/store"
	"github.com/pipozzz/goliash/internal/tokens"
	"github.com/pipozzz/goliash/internal/versions"
	"github.com/pipozzz/goliash/pkg/buildinfo"
)

// agentView is how an agent shows in lists and on its page.
func agentView(a store.Agent) AgentView {
	status := "online"
	switch {
	case a.ActiveTokens == 0:
		status = "revoked"
	case a.LastSeenAt.IsZero():
		status = "never"
	case !a.StaleSince.IsZero():
		status = "stale"
	}
	return AgentView{
		ID: a.ID, Name: a.Name, Status: status, Outdated: olderThanServer(a.Version), Version: a.Version,
		Hostname: a.Hostname, LastSeen: a.LastSeenAt, Platforms: strings.Join(a.Platforms, ", "),
	}
}

// olderThanServer reports whether an agent runs an older release than the server.
// Development builds compare as unknown.
func olderThanServer(agentVersion string) bool {
	server, ok := versions.ParseVersion(buildinfo.Version)
	if !ok || agentVersion == "" {
		return false
	}
	agent, ok := versions.ParseVersion(agentVersion)
	return ok && agent.Compare(server) < 0
}

func (s *Server) agent(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	return s.showAgent(w, r, p, r.PathValue("id"), "", false, "")
}

// showAgent renders an agent's page; token is a new token to show once.
func (s *Server) showAgent(w http.ResponseWriter, r *http.Request, p auth.Principal, id, token string, rotated bool, notice string) error {
	ctx := r.Context()
	a, err := s.store.GetAgent(ctx, p.Scope, id)
	if errors.Is(err, store.ErrNotFound) {
		return back(w, r, "/agents", "error", "Unknown agent.")
	}
	if err != nil {
		return err
	}
	all, err := s.agentsView(ctx, p)
	if err != nil {
		return err
	}
	v := AgentPageView{
		Base: withFlash(s.base(ctx, p, "agents", a.Name), r), Agent: agentView(a),
		Registered: a.RegisteredAt, Created: a.CreatedAt, Moves: all.Moves,
		NewToken: token, Rotated: rotated, ServerURL: s.publicURL, ServerVersion: buildinfo.Version,
	}
	if notice != "" {
		v.Notice = notice
	}
	if v.Tokens, err = s.store.AgentTokens(ctx, p.Scope, a.ID); err != nil {
		return err
	}
	for _, t := range all.Targets {
		if t.AgentID == a.ID {
			v.Targets = append(v.Targets, t)
		}
	}
	if token != "" {
		w.Header().Set("Cache-Control", "no-store")
	}
	return render(w, r, AgentPage(v))
}

func (s *Server) rotateAgent(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	ctx := r.Context()
	a, err := s.store.GetAgent(ctx, p.Scope, r.PathValue("id"))
	if err != nil {
		return back(w, r, "/agents", "error", "Unknown agent.")
	}
	token, hash := tokens.New(tokens.Agent)
	if err := s.store.AddAgentToken(ctx, p.Scope, a.ID, hash); err != nil {
		return err
	}
	s.audit(ctx, p, "agent.rotate_token", "agent", a.Name)
	toks, err := s.store.AgentTokens(ctx, p.Scope, a.ID)
	if err != nil {
		return err
	}
	pending := len(toks) > 1 // the agent still runs with an older token
	notice := "New token for " + a.Name + "."
	if pending {
		notice += " The old one works until the agent uses this one."
	}
	return s.showAgent(w, r, p, a.ID, token, pending, notice)
}

func (s *Server) revokeAgent(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	ctx := r.Context()
	a, err := s.store.GetAgent(ctx, p.Scope, r.PathValue("id"))
	if err != nil {
		return back(w, r, "/agents", "error", "Unknown agent.")
	}
	if err := s.store.RevokeAgentTokens(ctx, p.Scope, a.ID); err != nil {
		return err
	}
	s.audit(ctx, p, "agent.revoke", "agent", a.Name)
	return back(w, r, "/agents/"+a.ID, "notice", "Every token of "+a.Name+" is revoked. Rotate to connect it again.")
}

func (s *Server) renameAgent(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	ctx := r.Context()
	a, err := s.store.GetAgent(ctx, p.Scope, r.PathValue("id"))
	if err != nil {
		return back(w, r, "/agents", "error", "Unknown agent.")
	}
	name := strings.TrimSpace(r.FormValue("name"))
	if !serviceName.MatchString(name) {
		return back(w, r, "/agents/"+a.ID, "error", "Agent names use letters, digits, dots, dashes and underscores.")
	}
	if name == a.Name {
		return back(w, r, "/agents/"+a.ID, "notice", "Nothing changed.")
	}
	if err := s.store.RenameAgent(ctx, p.Scope, a.ID, name); errors.Is(err, store.ErrExists) {
		return back(w, r, "/agents/"+a.ID, "error", "Another agent is named "+name+".")
	} else if err != nil {
		return err
	}
	s.audit(ctx, p, "agent.rename", "from", a.Name, "to", name)
	return back(w, r, "/agents/"+a.ID, "notice", "Renamed to "+name+".")
}

func (s *Server) deleteAgent(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	ctx := r.Context()
	a, err := s.store.GetAgent(ctx, p.Scope, r.PathValue("id"))
	if err != nil {
		return back(w, r, "/agents", "error", "Unknown agent.")
	}
	if err := s.store.DeleteAgent(ctx, p.Scope, a.ID); errors.Is(err, store.ErrInUse) {
		return back(w, r, "/agents/"+a.ID, "error", "Move or delete the targets of "+a.Name+" first.")
	} else if err != nil {
		return err
	}
	s.audit(ctx, p, "agent.delete", "agent", a.Name)
	return back(w, r, "/agents", "notice", "Agent "+a.Name+" deleted.")
}

// moveTarget hands a target to another agent, or to the server ("server").
func (s *Server) moveTarget(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	ctx := r.Context()
	t, err := s.store.GetTarget(ctx, p.Scope, r.PathValue("id"))
	if err != nil {
		return back(w, r, "/agents", "error", "Unknown target.")
	}
	to, toName := r.FormValue("agent"), "the server"
	if to == "server" {
		to = ""
	} else if a, err := s.store.GetAgent(ctx, p.Scope, to); err == nil {
		toName = a.Name
	} else {
		return back(w, r, "/agents", "error", "Unknown agent.")
	}
	from := returnTo(r)
	if to == t.AgentID {
		return back(w, r, from, "notice", "Nothing changed.")
	}
	if err := s.store.SetTargetAgent(ctx, p.Scope, t.ID, to); err != nil {
		return err
	}
	s.audit(ctx, p, "target.move", "target", t.Name, "to", toName)
	return back(w, r, from, "notice", t.Name+" is collected by "+toName+" from its next configuration poll.")
}

func (s *Server) deleteTarget(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	ctx := r.Context()
	t, err := s.store.GetTarget(ctx, p.Scope, r.PathValue("id"))
	if err != nil {
		return back(w, r, "/agents", "error", "Unknown target.")
	}
	if err := s.store.DeleteTarget(ctx, p.Scope, t.ID); err != nil {
		return err
	}
	if s.checker != nil {
		_ = s.checker.EvaluateDrift(ctx, p.Scope)
	}
	s.hub.Publish(p.Scope.WorkspaceID)
	s.audit(ctx, p, "target.delete", "target", t.Name)
	return back(w, r, returnTo(r), "notice", "Target "+t.Name+" deleted.")
}

// returnTo is the agents page a target form came from: the list or one agent.
func returnTo(r *http.Request) string {
	from := r.FormValue("from")
	if from == "/agents" || (strings.HasPrefix(from, "/agents/") && !strings.ContainsAny(from[len("/agents/"):], "/?#\\")) {
		return from
	}
	return "/agents"
}
