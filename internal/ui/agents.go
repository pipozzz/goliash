// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package ui

import (
	"errors"
	"net/http"
	"net/url"
	"slices"
	"sort"
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
		Enrolled: a.CodeID != "", Notes: a.Notes, Where: whereItems(a.Where),
	}
}

// WhereItem is one thing known of where an agent or target runs: its label and value, and how many services the
// matrix finds by it (counted on the agent's page only).
type WhereItem struct {
	Key, Label, Value string
	Services          int
}

// whereKeys are the keys of where something runs, in the order shown, with their labels.
var whereKeys = []struct{ key, label string }{
	{"cloud", "Cloud"},
	{"account", "Account"},
	{"region", "Region"},
	{"zone", "Zone"},
	{"cluster", "Cluster"},
	{"instance", "Instance"},
	{"instance_type", "Instance type"},
	{"platform", "Platform"},
	{"os", "OS"},
	{"kernel", "Kernel"},
	{"arch", "Architecture"},
}

// whereItems lists where something runs in a steady order; keys Goliash does not know follow, sorted.
func whereItems(where map[string]string) []WhereItem {
	var out []WhereItem
	known := map[string]bool{}
	for _, k := range whereKeys {
		known[k.key] = true
		if v := where[k.key]; v != "" {
			out = append(out, WhereItem{Key: k.key, Label: k.label, Value: v})
		}
	}
	var rest []string
	for k, v := range where {
		if !known[k] && v != "" {
			rest = append(rest, k)
		}
	}
	sort.Strings(rest)
	for _, k := range rest {
		out = append(out, WhereItem{Key: k, Label: k, Value: where[k]})
	}
	return out
}

// whereShort is where something runs in a few words: "aws 123456789012 · eu-west-1a · t3.medium".
func whereShort(items []WhereItem) string {
	var parts []string
	for _, it := range items {
		switch it.Key {
		case "account", "zone", "instance_type", "os":
			parts = append(parts, it.Value)
		case "region":
			if !slices.ContainsFunc(items, func(i WhereItem) bool { return i.Key == "zone" }) {
				parts = append(parts, it.Value)
			}
		}
	}
	return strings.Join(parts, " · ")
}

// whereWord is the matrix filter word for an item: its key and the first word of its value, "os:ubuntu".
func whereWord(it WhereItem) string {
	return it.Key + ":" + strings.ToLower(strings.Fields(it.Value + " ")[0])
}

// whereFilter is the matrix filtered to what runs where an item says.
func whereFilter(it WhereItem) string { return "/?view=table&q=" + url.QueryEscape(whereWord(it)) }

// countWhere counts, for each item, the services of the matrix its filter word finds. What the agent learnt of its
// own machine (zone, instance, OS …) finds nothing when it watches only through cloud APIs: ECS tasks, RDS
// databases and the like run elsewhere, and keep only the cloud and account.
func countWhere(items []WhereItem, rows []MatrixRow) {
	for i := range items {
		word, found := whereWord(items[i]), map[string]bool{}
		for _, r := range rows {
			if slices.ContainsFunc(r.Where, func(t string) bool { return strings.Contains(t, word) }) {
				found[r.Service] = true
			}
		}
		items[i].Services = len(found)
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
	if v.Found, err = s.foundOf(ctx, p.Scope, a); err != nil {
		return err
	}

	v.EnvNames = all.EnvNames
	var collects []string
	for _, t := range all.Targets {
		if t.AgentID == a.ID {
			v.Targets = append(v.Targets, t)
			collects = append(collects, t.Platform)
		}
	}
	v.Sources = agentSources(agentRuns(collects), collects, v.Found, a.Notes)
	if len(v.Agent.Where) > 0 {
		g, err := s.overview(ctx, p.Scope, "")
		if err != nil {
			return err
		}
		countWhere(v.Agent.Where, g.Rows)
	}
	if v.Agent.Outdated {
		for _, t := range v.Targets {
			if v.UpgradeTitle, v.UpgradeCmd = upgradeCommand(t.Platform, s.publicURL, buildinfo.Version); v.UpgradeCmd != "" {
				break
			}
		}
	}
	if a.CodeID != "" {
		if c, err := s.store.GetEnrollmentCode(ctx, p.Scope, a.CodeID); err == nil {
			v.Code = CodeInfo{Many: !c.Single, Revoked: !c.RevokedAt.IsZero()}
			for _, e := range all.Envs {
				if e.ID == c.EnvironmentID {
					v.Code.Env = e.Name
				}
			}
		}
		ts, err := s.store.ListAgentTargets(ctx, p.Scope, a.ID)
		if err != nil {
			return err
		}
		for _, t := range ts {
			if t.AgentKey != "" {
				v.Managed++
			}
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
	// The targets an enrolled agent found itself go with it; others have to be moved
	// or deleted first.
	ts, err := s.store.ListAgentTargets(ctx, p.Scope, a.ID)
	if err != nil {
		return err
	}
	for _, t := range ts {
		if t.AgentKey == "" {
			continue
		}
		if err := s.store.DeleteTarget(ctx, p.Scope, t.ID); err != nil {
			return err
		}
		s.audit(ctx, p, "target.delete", "target", t.Name, "agent", a.Name)
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

// whereSearch is what a page filter matches where something runs against: "region:eu-central-1 os:ubuntu 26.04 lts".
func whereSearch(items []WhereItem) string {
	parts := make([]string, 0, len(items))
	for _, it := range items {
		parts = append(parts, it.Key+":"+strings.ToLower(it.Value))
	}
	return strings.Join(parts, " ")
}

// agentSearch is what the agents page filter matches an agent against.
func agentSearch(a AgentView) string {
	parts := []string{a.Name, a.Status, a.Version, a.Hostname, a.Collects, whereSearch(a.Where)}
	if a.Outdated {
		parts = append(parts, "outdated update available")
	}
	return strings.ToLower(strings.Join(parts, " "))
}

// targetSearch is what the agents page filter matches a target against.
func targetSearch(t TargetView) string {
	words := []string{t.Name, t.Platform, t.Env, t.Agent, t.Status, whereSearch(t.Where)}
	if t.Machine != "" {
		words = append(words, t.Machine)
	}
	return strings.ToLower(strings.Join(words, " "))
}

// oneMachine shows a machine an agent collects twice (its Docker engine, and its operating system and packages as
// a Linux host) as one: the host right under the engine, as its part. An agent with more than one engine, or more
// than one host, keeps its targets apart.
func oneMachine(ts []TargetView) []TargetView {
	engines, hosts := map[string][]int{}, map[string][]int{}
	for i, t := range ts {
		if t.AgentID == "" {
			continue
		}
		switch t.Platform {
		case "docker":
			engines[t.AgentID] = append(engines[t.AgentID], i)
		case "host":
			hosts[t.AgentID] = append(hosts[t.AgentID], i)
		}
	}
	part := map[int]int{} // host index -> engine index
	for agent, hs := range hosts {
		// The same machine: the agent named both after its hostname (ip-10-0-0-5, and ip-10-0-0-5-2 for the second).
		if es := engines[agent]; len(es) == 1 && len(hs) == 1 && sameMachine(ts[es[0]].Name, ts[hs[0]].Name) {
			part[hs[0]] = es[0]
			ts[hs[0]].Machine = ts[es[0]].Name
		}
	}
	if len(part) == 0 {
		return ts
	}
	under := map[int]int{}
	for h, e := range part {
		under[e] = h
	}
	out := make([]TargetView, 0, len(ts))
	for i, t := range ts {
		if _, isPart := part[i]; isPart {
			continue
		}
		out = append(out, t)
		if h, ok := under[i]; ok {
			out = append(out, ts[h])
		}
	}
	return out
}

// platformLabel names a platform as the matrix's filter does (Linux host for host), or as it is.
func platformLabel(p string) string {
	if k, ok := kindOf(p); ok {
		return k.Label
	}
	return p
}

// sameMachine tells two targets an agent named after one hostname: the same name, or the second one Goliash told
// apart with a number (ip-10-0-0-5-2), or with -os.
func sameMachine(engine, host string) bool {
	if engine == host || host == engine+"-os" {
		return true
	}
	rest, ok := strings.CutPrefix(host, engine+"-")
	if !ok || rest == "" {
		return false
	}
	return strings.Trim(rest, "0123456789") == ""
}
