// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package ui

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/a-h/templ"

	"github.com/pipozzz/goliash/internal/auth"
	"github.com/pipozzz/goliash/internal/notifier"
	"github.com/pipozzz/goliash/internal/store"
	"github.com/pipozzz/goliash/internal/tokens"
	"github.com/pipozzz/goliash/internal/versions"
)

//go:embed static
var static embed.FS

// assetVersion changes whenever an embedded asset changes, so browsers can cache
// /static files for long and still pick up new ones after an upgrade.
var assetVersion = func() string {
	h := sha256.New()
	_ = fs.WalkDir(static, "static", func(path string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			b, _ := static.ReadFile(path)
			h.Write(b)
		}
		return nil
	})
	return hex.EncodeToString(h.Sum(nil))[:12]
}()

func asset(name string) string { return "/static/" + name + "?v=" + assetVersion }

// Server renders the web UI.
type Server struct {
	store     *store.Store
	auth      *auth.Auth
	checker   *versions.Checker
	notify    *notifier.Notifier
	hub       *Hub
	log       *slog.Logger
	publicURL string
	smtp      bool
}

// Options wire the UI to the rest of the server.
type Options struct {
	Store     *store.Store
	Auth      *auth.Auth
	Checker   *versions.Checker
	Notifier  *notifier.Notifier
	Hub       *Hub
	Log       *slog.Logger
	PublicURL string
	SMTP      bool // whether e-mail is configured
}

// New returns the UI server.
func New(o Options) *Server {
	return &Server{
		store: o.Store, auth: o.Auth, checker: o.Checker, notify: o.Notifier, hub: o.Hub, log: o.Log,
		publicURL: strings.TrimSuffix(o.PublicURL, "/"), smtp: o.SMTP,
	}
}

// Register adds the UI routes to mux.
func (s *Server) Register(mux *http.ServeMux) {
	sub, _ := fs.Sub(static, "static")
	files := http.StripPrefix("/static/", http.FileServer(http.FS(sub)))
	mux.Handle("GET /static/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		files.ServeHTTP(w, r)
	}))
	mux.HandleFunc("GET /login", s.login)

	v, m, a := store.RoleViewer, store.RoleMember, store.RoleAdmin
	mux.Handle("GET /{$}", s.page(v, s.matrix))
	mux.Handle("GET /ui/matrix", s.page(v, s.matrixGrid))
	mux.Handle("GET /ui/stream", s.page(v, s.stream))
	mux.Handle("GET /services/{name}", s.page(v, s.service))
	mux.Handle("POST /services/{name}/policy", s.page(m, s.savePolicy))
	mux.Handle("POST /services/{name}/ack", s.page(m, s.ack))
	mux.Handle("POST /services/{name}/check", s.page(m, s.checkNow))
	mux.Handle("GET /events", s.page(v, s.events))
	mux.Handle("GET /inbox", s.page(v, s.inbox))
	mux.Handle("POST /inbox/map", s.page(m, s.inboxMap))
	mux.Handle("POST /inbox/ignore", s.page(m, s.inboxIgnore))
	mux.Handle("GET /agents", s.page(v, s.agents))
	mux.Handle("POST /agents", s.page(a, s.createAgent))
	mux.Handle("POST /environments", s.page(a, s.createEnvironment))
	mux.Handle("POST /targets", s.page(a, s.createTarget))
	mux.Handle("GET /notifications", s.page(v, s.notifications))
	mux.Handle("POST /notifications/channels", s.page(a, s.createChannel))
	mux.Handle("POST /notifications/channels/{id}/test", s.page(a, s.testChannel))
	mux.Handle("POST /notifications/rules", s.page(m, s.createRule))
	mux.Handle("GET /settings", s.page(a, s.settings))
	mux.Handle("POST /settings/users", s.page(a, s.inviteUser))
	mux.Handle("POST /settings/users/{id}/role", s.page(a, s.setRole))
	mux.Handle("POST /settings/users/{id}/link", s.page(a, s.userLink))
	mux.Handle("POST /settings/users/{id}/delete", s.page(a, s.deleteUser))
	mux.Handle("POST /settings/tokens", s.page(a, s.createToken))
}

type handler func(w http.ResponseWriter, r *http.Request, p auth.Principal) error

// page authenticates, checks the role and turns handler errors into a page.
func (s *Server) page(role string, h handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, ok, err := s.auth.Authenticate(r)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		if !ok || p.Via != "session" {
			if r.Header.Get("HX-Request") == "true" {
				w.Header().Set("HX-Redirect", "/login")
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		if !p.Can(role) {
			http.Error(w, "Your role ("+p.Role+") does not allow this. Ask an admin.", http.StatusForbidden)
			return
		}
		if err := h(w, r, p); err != nil {
			s.fail(w, r, err)
		}
	})
}

func (s *Server) fail(w http.ResponseWriter, r *http.Request, err error) {
	s.log.ErrorContext(r.Context(), "ui request failed", "path", r.URL.Path, "err", err)
	http.Error(w, "Something went wrong. The error is in the server log.", http.StatusInternalServerError)
}

func (s *Server) base(ctx context.Context, p auth.Principal, page, title string) Base {
	b := Base{
		Title: title, Page: page, Email: p.User.Email, Role: p.Role,
		CanMember: p.Can(store.RoleMember), CanAdmin: p.Can(store.RoleAdmin),
	}
	if items, err := s.inboxItems(ctx, p.Scope); err == nil {
		b.InboxCount = len(items)
	}
	return b
}

// withFlash fills notice and error from the query string set by redirects.
func withFlash(b Base, r *http.Request) Base {
	b.Notice, b.Error = r.URL.Query().Get("notice"), r.URL.Query().Get("error")
	return b
}

// back redirects to a page of this UI with a notice or error. path is always built
// by the handlers from fixed prefixes; anything else falls back to the home page.
func back(w http.ResponseWriter, r *http.Request, path, key, msg string) error {
	if !strings.HasPrefix(path, "/") || strings.HasPrefix(path, "//") || strings.HasPrefix(path, "/\\") {
		path = "/"
	}
	sep := "?"
	if strings.Contains(path, "?") {
		sep = "&"
	}
	http.Redirect(w, r, path+sep+key+"="+url.QueryEscape(msg), http.StatusSeeOther) //nolint:gosec // same-site path, checked above
	return nil
}

func render(w http.ResponseWriter, r *http.Request, c templ.Component) error {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	return c.Render(r.Context(), w)
}

// ---- sign-in ----

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	if _, ok, _ := s.auth.Authenticate(r); ok {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	v := LoginView{Sent: r.URL.Query().Get("sent") == "1", Mail: s.auth.MailEnabled(), OIDC: s.auth.OIDCName()}
	switch r.URL.Query().Get("error") {
	case "link":
		v.Error = "That sign-in link is used or expired. Ask for a new one."
	case "oidc":
		v.Error = "Single sign-on did not work for this account. Ask an admin to invite you."
	}
	_ = render(w, r, Login(v))
}

// ---- matrix ----

func (s *Server) overview(ctx context.Context, sc store.Scope) (MatrixGrid, error) {
	o, err := versions.LoadOverview(ctx, s.store, sc)
	if err != nil {
		return MatrixGrid{}, err
	}
	agents, err := s.store.ListAgents(ctx, sc)
	if err != nil {
		return MatrixGrid{}, err
	}
	return buildGrid(o, len(agents)), nil
}

func (s *Server) matrix(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	g, err := s.overview(r.Context(), p.Scope)
	if err != nil {
		return err
	}
	return render(w, r, MatrixPage(MatrixView{Base: withFlash(s.base(r.Context(), p, "matrix", "Matrix"), r), Grid: g}))
}

func (s *Server) matrixGrid(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	g, err := s.overview(r.Context(), p.Scope)
	if err != nil {
		return err
	}
	return render(w, r, MatrixGridView(g))
}

// stream sends "changed" whenever the workspace's data changes (server-sent events).
func (s *Server) stream(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	flusher, ok := w.(http.Flusher)
	if !ok {
		return errors.New("streaming unsupported")
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	ch, cancel := s.hub.Subscribe(p.Scope.WorkspaceID)
	defer cancel()
	_, _ = fmt.Fprint(w, "retry: 5000\n\n")
	flusher.Flush()
	ping := time.NewTicker(25 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-r.Context().Done():
			return nil
		case <-ch:
			_, _ = fmt.Fprint(w, "event: changed\ndata: {}\n\n")
		case <-ping.C:
			_, _ = fmt.Fprint(w, ": ping\n\n")
		}
		flusher.Flush()
	}
}

// ---- service ----

func (s *Server) service(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	ctx := r.Context()
	name := r.PathValue("name")
	svc, err := s.store.GetServiceByName(ctx, p.Scope, name)
	if errors.Is(err, store.ErrNotFound) {
		http.NotFound(w, r)
		return nil
	}
	if err != nil {
		return err
	}
	o, err := versions.LoadOverview(ctx, s.store, p.Scope)
	if err != nil {
		return err
	}
	v := ServiceView{Base: withFlash(s.base(ctx, p, "matrix", svc.Name), r), Name: svc.Name, Owner: svc.Owner, Kind: svc.Kind, Upstream: svc.Upstream}
	ref := o.Refs[svc.ID]
	v.RefRepo = ref.Repo
	v.Private = ref.Repo != "" && !versions.IsPublicRegistry(ref.Repo)
	if u, ok := o.Upstreams[svc.ID]; ok {
		if u.HasLatest {
			v.Latest = u.Latest.Raw
		}
		if u.HasLatestAny {
			v.LatestAny = u.LatestAny.Raw
		}
	}
	v.CheckedAt, v.CheckError, _ = s.store.UpstreamStatus(ctx, p.Scope, svc.ID)
	pol := o.Policies[svc.ID]
	if pol.Track == "" {
		pol, _ = versions.ParsePolicy(svc.VersionPolicy)
	}
	v.Policy = PolicyForm{TagFilter: pol.TagFilter, Track: string(pol.Track), Prerelease: pol.Prerelease}
	if pol.PinMajor != nil {
		v.Policy.PinMajor = strconv.Itoa(*pol.PinMajor)
	}

	var row *versions.Row
	for i := range o.Matrix.Rows {
		if o.Matrix.Rows[i].Service.ID == svc.ID {
			row = &o.Matrix.Rows[i]
		}
	}
	for ei, e := range o.Matrix.Environments {
		se := ServiceEnv{Name: e.Name}
		if row != nil {
			for _, ver := range row.Cells[ei].Versions {
				se.Versions = append(se.Versions, VersionView{Tag: ver.Tag, Running: ver.Running, Targets: strings.Join(ver.Targets, ", ")})
			}
		}
		for _, d := range o.DriftsAt(svc.ID, e.ID) {
			se.Drifts = append(se.Drifts, driftBadge(d))
		}
		v.Envs = append(v.Envs, se)
	}

	releases, err := s.store.ListReleases(ctx, p.Scope, svc.ID)
	if err != nil {
		return err
	}
	var parsed []versions.Version
	for _, rel := range releases {
		if pv, ok := versions.ParseVersion(rel.Version); ok {
			parsed = append(parsed, pv)
		}
	}
	sort.Slice(parsed, func(i, j int) bool { return parsed[i].Compare(parsed[j]) > 0 })
	for i := 0; i < len(parsed) && i < 12; i++ {
		v.Releases = append(v.Releases, parsed[i].Raw)
	}

	evs, err := s.store.ListEvents(ctx, p.Scope, store.EventFilter{ServiceID: svc.ID, Limit: 25})
	if err != nil {
		return err
	}
	v.Events = eventViews(evs, o)

	acks, err := s.store.ListAcks(ctx, p.Scope)
	if err != nil {
		return err
	}
	for _, a := range acks {
		if a.ServiceID != svc.ID {
			continue
		}
		av := AckView{Kind: a.Kind, Env: o.Envs[a.EnvironmentID].Name, By: a.CreatedBy, Created: a.CreatedAt}
		switch {
		case a.UntilVersion != "" && !a.UntilAt.IsZero():
			av.Until = a.UntilVersion + " or " + a.UntilAt.Format("2006-01-02")
		case a.UntilVersion != "":
			av.Until = "version " + a.UntilVersion
		default:
			av.Until = a.UntilAt.Format("2006-01-02 15:04 UTC")
		}
		av.Active = a.UntilAt.IsZero() || time.Now().Before(a.UntilAt)
		v.Acks = append(v.Acks, av)
	}
	return render(w, r, ServicePage(v))
}

func (s *Server) savePolicy(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	ctx := r.Context()
	name := r.PathValue("name")
	path := serviceURL(name)
	svc, err := s.store.GetServiceByName(ctx, p.Scope, name)
	if err != nil {
		return back(w, r, "/", "error", "Unknown service")
	}
	pol := versions.Policy{
		TagFilter: strings.TrimSpace(r.FormValue("tag_filter")), Track: versions.Jump(r.FormValue("track")),
		Prerelease: r.FormValue("prerelease") == "1",
	}
	if pm := strings.TrimSpace(r.FormValue("pin_major")); pm != "" {
		n, err := strconv.Atoi(pm)
		if err != nil || n < 0 {
			return back(w, r, path, "error", "Pin major must be a whole number, e.g. 15.")
		}
		pol.PinMajor = &n
	}
	raw, _ := json.Marshal(pol)
	if _, err := versions.ParsePolicy(raw); err != nil {
		return back(w, r, path, "error", "Policy not saved: "+err.Error())
	}
	kind := r.FormValue("kind")
	if kind != "third_party" {
		kind = "own"
	}
	svc.Owner, svc.Kind, svc.Upstream, svc.VersionPolicy = strings.TrimSpace(r.FormValue("owner")), kind,
		strings.TrimSpace(r.FormValue("upstream")), raw
	if err := s.store.UpdateService(ctx, svc); err != nil {
		return err
	}
	if s.checker != nil {
		_ = s.checker.EvaluateDrift(ctx, p.Scope)
	}
	s.hub.Publish(p.Scope.WorkspaceID)
	return back(w, r, path, "notice", "Policy saved. Drift was re-evaluated with it.")
}

func (s *Server) ack(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	ctx := r.Context()
	name := r.PathValue("name")
	path := serviceURL(name)
	svc, err := s.store.GetServiceByName(ctx, p.Scope, name)
	if err != nil {
		return back(w, r, "/", "error", "Unknown service")
	}
	a := store.Ack{Scope: p.Scope, ServiceID: svc.ID, Kind: r.FormValue("kind"), UntilVersion: strings.TrimSpace(r.FormValue("until_version")), CreatedBy: p.Name()}
	if a.Kind != "release" && a.Kind != "drift" {
		return back(w, r, path, "error", "Choose releases or drift.")
	}
	if d, err := time.ParseDuration(r.FormValue("for")); err == nil && d > 0 {
		a.UntilAt = time.Now().Add(d)
	}
	if a.UntilVersion == "" && a.UntilAt.IsZero() {
		return back(w, r, path, "error", "Give a version or a duration.")
	}
	if _, err := s.store.CreateAck(ctx, a); err != nil {
		return err
	}
	return back(w, r, path, "notice", "Acknowledged. Matching notifications stay quiet.")
}

func (s *Server) checkNow(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	ctx := r.Context()
	name := r.PathValue("name")
	path := serviceURL(name)
	svc, err := s.store.GetServiceByName(ctx, p.Scope, name)
	if err != nil || s.checker == nil {
		return back(w, r, "/", "error", "Unknown service")
	}
	if err := s.checker.CheckService(ctx, p.Scope, svc.ID); err != nil {
		return back(w, r, path, "error", "Upstream check failed: "+err.Error())
	}
	s.hub.Publish(p.Scope.WorkspaceID)
	return back(w, r, path, "notice", "Upstream checked.")
}

// ---- history ----

var eventTypes = []string{"deployed", "version_changed", "removed", "new_release", "drift_detected", "drift_resolved"}

func (s *Server) events(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	ctx := r.Context()
	q := r.URL.Query()
	v := EventsView{Service: q.Get("service"), Env: q.Get("environment"), Type: q.Get("type"), Types: eventTypes}
	f := store.EventFilter{Limit: 50}
	if v.Type != "" {
		f.Types = []string{v.Type}
	}
	if b := q.Get("before"); b != "" {
		if t, err := time.Parse(time.RFC3339Nano, b); err == nil {
			f.Before = t
		}
	}
	o, err := versions.LoadOverview(ctx, s.store, p.Scope)
	if err != nil {
		return err
	}
	for _, svc := range o.Services {
		v.Services = append(v.Services, svc.Name)
		if svc.Name == v.Service {
			f.ServiceID = svc.ID
		}
	}
	sort.Strings(v.Services)
	for _, e := range o.Matrix.Environments {
		v.EnvNames = append(v.EnvNames, e.Name)
		if e.Name == v.Env {
			f.EnvironmentID = e.ID
		}
	}
	evs, err := s.store.ListEvents(ctx, p.Scope, f)
	if err != nil {
		return err
	}
	v.Events = eventViews(evs, o)
	if len(evs) == f.Limit {
		more := url.Values{"before": {evs[len(evs)-1].At.Format(time.RFC3339Nano)}}
		for k, val := range map[string]string{"service": v.Service, "environment": v.Env, "type": v.Type} {
			if val != "" {
				more.Set(k, val)
			}
		}
		v.MoreURL = "/events?" + more.Encode()
	}
	if r.Header.Get("HX-Request") == "true" && q.Get("before") != "" {
		return render(w, r, EventsChunk(v))
	}
	v.Base = withFlash(s.base(ctx, p, "events", "History"), r)
	return render(w, r, EventsPage(v))
}

// ---- inbox ----

func (s *Server) inboxItems(ctx context.Context, sc store.Scope) ([]InboxItem, error) {
	active, err := s.store.ListActiveInstances(ctx, sc)
	if err != nil {
		return nil, err
	}
	targets, err := s.store.ListTargets(ctx, sc)
	if err != nil {
		return nil, err
	}
	envs, err := s.store.ListEnvironments(ctx, sc)
	if err != nil {
		return nil, err
	}
	tName, envName := map[string]string{}, map[string]string{}
	for _, t := range targets {
		tName[t.ID] = t.Name
	}
	for _, e := range envs {
		envName[e.ID] = e.Name
	}
	seen := map[string]bool{}
	var items []InboxItem
	for _, i := range active {
		if i.ServiceID != "" || !i.IsMain || seen[i.TargetID+"/"+i.WorkloadID] {
			continue
		}
		seen[i.TargetID+"/"+i.WorkloadID] = true
		ref := versions.ParseImage(i.Image)
		items = append(items, InboxItem{
			TargetID: i.TargetID, WorkloadID: i.WorkloadID, Target: tName[i.TargetID],
			Env: envName[i.EnvironmentID], Namespace: i.Namespace, Workload: i.WorkloadName, Kind: i.WorkloadKind,
			Image: i.Image, Repo: ref.Repo(), Suggested: i.SuggestedService,
		})
	}
	sort.Slice(items, func(a, b int) bool { return items[a].Workload < items[b].Workload })
	return items, nil
}

func (s *Server) inbox(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	ctx := r.Context()
	items, err := s.inboxItems(ctx, p.Scope)
	if err != nil {
		return err
	}
	svcs, err := s.store.ListServices(ctx, p.Scope)
	if err != nil {
		return err
	}
	v := InboxView{Base: withFlash(s.base(ctx, p, "inbox", "Inbox"), r), Items: items}
	for _, svc := range svcs {
		v.Services = append(v.Services, svc.Name)
	}
	return render(w, r, InboxPage(v))
}

var serviceName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,99}$`)

// inboxMap maps a workload now and adds an image rule so later instances of the
// same image map by themselves.
func (s *Server) inboxMap(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	ctx := r.Context()
	name := strings.TrimSpace(r.FormValue("service"))
	if !serviceName.MatchString(name) {
		return back(w, r, "/inbox", "error", "Service names use letters, digits, dots, dashes and underscores.")
	}
	svc, err := s.store.EnsureService(ctx, p.Scope, name)
	if err != nil {
		return err
	}
	if repo := r.FormValue("repo"); repo != "" {
		if _, err := s.store.CreateMappingRule(ctx, store.MappingRule{
			Scope: p.Scope, Priority: 100, MatchType: "image_repo",
			Pattern: regexp.QuoteMeta(repo), ServiceID: svc.ID,
		}); err != nil {
			return err
		}
	}
	if err := s.store.MapInstances(ctx, p.Scope, r.FormValue("target_id"), r.FormValue("workload_id"), svc.ID); err != nil {
		return err
	}
	if s.checker != nil {
		_ = s.checker.EvaluateDrift(ctx, p.Scope)
	}
	s.hub.Publish(p.Scope.WorkspaceID)
	return back(w, r, "/inbox", "notice", "Mapped to "+svc.Name+". Other workloads running this image map to it from the next snapshot.")
}

func (s *Server) inboxIgnore(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	repo := r.FormValue("repo")
	if repo == "" {
		return back(w, r, "/inbox", "error", "Nothing to ignore.")
	}
	if _, err := s.store.CreateMappingRule(r.Context(), store.MappingRule{
		Scope: p.Scope, Priority: 0, MatchType: "ignore",
		Pattern: regexp.QuoteMeta(repo),
	}); err != nil {
		return err
	}
	return back(w, r, "/inbox", "notice", repo+" is ignored from the next snapshot.")
}

// ---- agents and targets ----

func (s *Server) agentsView(ctx context.Context, p auth.Principal) (AgentsView, error) {
	v := AgentsView{Base: s.base(ctx, p, "agents", "Agents"), ServerURL: s.publicURL}
	agents, err := s.store.ListAgents(ctx, p.Scope)
	if err != nil {
		return v, err
	}
	agentName := map[string]string{}
	for _, a := range agents {
		agentName[a.ID] = a.Name
		v.AgentNames = append(v.AgentNames, a.Name)
		status := "online"
		switch {
		case a.LastSeenAt.IsZero():
			status = "never"
		case !a.StaleSince.IsZero():
			status = "stale"
		}
		av := AgentView{
			Name: a.Name, Status: status, Version: a.Version, Hostname: a.Hostname, LastSeen: a.LastSeenAt,
			Platforms: strings.Join(a.Platforms, ", "),
		}
		v.Agents = append(v.Agents, av)
	}
	envs, err := s.store.ListEnvironments(ctx, p.Scope)
	if err != nil {
		return v, err
	}
	envName := map[string]string{}
	for _, e := range envs {
		envName[e.ID] = e.Name
		v.EnvNames = append(v.EnvNames, e.Name)
	}
	targets, err := s.store.ListTargets(ctx, p.Scope)
	if err != nil {
		return v, err
	}
	for _, t := range targets {
		by := agentName[t.AgentID]
		if t.AgentID == "" {
			by = "server"
		}
		v.Targets = append(v.Targets, TargetView{
			Name: t.Name, Platform: t.Platform, Env: envName[t.EnvironmentID], Agent: by,
			Status: t.CollectorStatus, Error: t.CollectorError, LastSnapshot: t.LastSnapshotAt,
		})
	}
	return v, nil
}

func (s *Server) agents(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	v, err := s.agentsView(r.Context(), p)
	if err != nil {
		return err
	}
	v.Base = withFlash(v.Base, r)
	return render(w, r, AgentsPage(v))
}

func (s *Server) createAgent(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	ctx := r.Context()
	name := strings.TrimSpace(r.FormValue("name"))
	if !serviceName.MatchString(name) {
		return back(w, r, "/agents", "error", "Agent names use letters, digits, dots, dashes and underscores.")
	}
	token, hash := tokens.New(tokens.Agent)
	if _, err := s.store.CreateAgent(ctx, p.Scope, name, hash); err != nil {
		return back(w, r, "/agents", "error", "Could not create the agent; is the name taken?")
	}
	v, err := s.agentsView(ctx, p)
	if err != nil {
		return err
	}
	v.NewToken, v.NewAgent = token, name
	v.Notice = "Agent " + name + " created. Copy its token now."
	w.Header().Set("Cache-Control", "no-store")
	return render(w, r, AgentsPage(v))
}

func (s *Server) createEnvironment(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	name := strings.TrimSpace(r.FormValue("name"))
	if !serviceName.MatchString(name) {
		return back(w, r, "/agents", "error", "Environment names use letters, digits, dots, dashes and underscores.")
	}
	pos, _ := strconv.Atoi(r.FormValue("position"))
	if _, err := s.store.CreateEnvironment(r.Context(), p.Scope, name, pos); err != nil {
		return back(w, r, "/agents", "error", "Could not create the environment; is the name taken?")
	}
	return back(w, r, "/agents", "notice", "Environment "+name+" created.")
}

func (s *Server) createTarget(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	ctx := r.Context()
	name := strings.TrimSpace(r.FormValue("name"))
	if !serviceName.MatchString(name) {
		return back(w, r, "/agents", "error", "Target names use letters, digits, dots, dashes and underscores.")
	}
	platform := r.FormValue("platform")
	switch platform {
	case "kubernetes", "ecs", "nomad", "swarm":
	default:
		return back(w, r, "/agents", "error", "Unknown platform.")
	}
	settings := strings.TrimSpace(r.FormValue("settings"))
	if settings == "" {
		settings = "{}"
	}
	var probe map[string]json.RawMessage
	if err := json.Unmarshal([]byte(settings), &probe); err != nil {
		return back(w, r, "/agents", "error", "Settings must be a JSON object.")
	}
	env, err := s.store.GetEnvironmentByName(ctx, p.Scope, r.FormValue("environment"))
	if err != nil {
		return back(w, r, "/agents", "error", "Unknown environment.")
	}
	t := store.Target{Scope: p.Scope, EnvironmentID: env.ID, Platform: platform, Name: name, Settings: json.RawMessage(settings)}
	if agent := r.FormValue("agent"); agent != "" {
		a, err := s.store.GetAgentByName(ctx, p.Scope, agent)
		if err != nil {
			return back(w, r, "/agents", "error", "Unknown agent.")
		}
		t.AgentID = a.ID
	}
	if _, err := s.store.CreateTarget(ctx, t); err != nil {
		return back(w, r, "/agents", "error", "Could not create the target; is the name taken?")
	}
	return back(w, r, "/agents", "notice", "Target "+name+" created. The agent picks it up within a minute.")
}

// ---- notifications ----

func (s *Server) notifications(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	ctx := r.Context()
	v := NotificationsView{Base: withFlash(s.base(ctx, p, "notifications", "Notifications"), r), SMTP: s.smtp}
	chans, err := s.store.ListChannels(ctx, p.Scope)
	if err != nil {
		return err
	}
	chName := map[string]string{}
	for _, c := range chans {
		chName[c.ID] = c.Name
		var cfg struct {
			URL string   `json:"url"`
			To  []string `json:"to"`
		}
		_ = json.Unmarshal(c.Config, &cfg)
		detail := strings.Join(cfg.To, ", ")
		if cfg.URL != "" {
			if u, err := url.Parse(cfg.URL); err == nil {
				detail = u.Host // never show the full webhook URL: it is a secret
			}
		}
		v.Channels = append(v.Channels, ChannelView{ID: c.ID, Name: c.Name, Type: c.Type, Detail: detail})
	}
	rules, err := s.store.ListRules(ctx, p.Scope)
	if err != nil {
		return err
	}
	for _, rule := range rules {
		var f notifier.Filter
		_ = json.Unmarshal(rule.Filter, &f)
		var parts []string
		if len(f.Services) > 0 {
			parts = append(parts, "services "+strings.Join(f.Services, ", "))
		}
		if len(f.Owners) > 0 {
			parts = append(parts, "owners "+strings.Join(f.Owners, ", "))
		}
		if len(f.Environments) > 0 {
			parts = append(parts, "in "+strings.Join(f.Environments, ", "))
		}
		if f.MinJump != "" {
			parts = append(parts, "releases from "+string(f.MinJump))
		}
		events := strings.Join(rule.EventTypes, ", ")
		if events == "" {
			events = "everything"
		}
		mode := rule.Mode
		if mode != "instant" && f.DigestHour != nil {
			mode += fmt.Sprintf(" at %02d:00 UTC", *f.DigestHour)
		}
		v.Rules = append(v.Rules, RuleView{Channel: chName[rule.ChannelID], Events: events, Mode: mode, Filter: orDash(strings.Join(parts, "; "))})
	}
	return render(w, r, NotificationsPage(v))
}

func (s *Server) createChannel(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	name := strings.TrimSpace(r.FormValue("name"))
	typ := r.FormValue("type")
	cfg := map[string]any{}
	switch typ {
	case "slack", "webhook":
		u, err := url.Parse(strings.TrimSpace(r.FormValue("url")))
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
			return back(w, r, "/notifications", "error", "Enter the full URL, starting with https://.")
		}
		cfg["url"] = u.String()
		if secret := r.FormValue("secret"); secret != "" && typ == "webhook" {
			cfg["secret"] = secret
		}
	case "email":
		var to []string
		for _, a := range strings.Split(r.FormValue("to"), ",") {
			if a = strings.TrimSpace(a); a != "" {
				to = append(to, a)
			}
		}
		if len(to) == 0 {
			return back(w, r, "/notifications", "error", "Enter at least one recipient.")
		}
		cfg["to"] = to
	default:
		return back(w, r, "/notifications", "error", "Unknown channel type.")
	}
	if name == "" {
		return back(w, r, "/notifications", "error", "Give the channel a name.")
	}
	raw, _ := json.Marshal(cfg)
	if _, err := s.store.CreateChannel(r.Context(), store.Channel{Scope: p.Scope, Type: typ, Name: name, Config: raw}); err != nil {
		return back(w, r, "/notifications", "error", "Could not add the channel; is the name taken?")
	}
	return back(w, r, "/notifications", "notice", "Channel "+name+" added. Send a test to check it.")
}

func (s *Server) testChannel(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	chans, err := s.store.ListChannels(r.Context(), p.Scope)
	if err != nil {
		return err
	}
	for _, c := range chans {
		if c.ID != r.PathValue("id") {
			continue
		}
		if err := s.notify.SendTest(r.Context(), c, "Goliash"); err != nil {
			return back(w, r, "/notifications", "error", "Test to "+c.Name+" failed: "+err.Error())
		}
		return back(w, r, "/notifications", "notice", "Test sent to "+c.Name+".")
	}
	return back(w, r, "/notifications", "error", "Unknown channel.")
}

func (s *Server) createRule(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	ctx := r.Context()
	if err := r.ParseForm(); err != nil {
		return err
	}
	ch, err := s.store.GetChannelByName(ctx, p.Scope, r.FormValue("channel"))
	if err != nil {
		return back(w, r, "/notifications", "error", "Unknown channel.")
	}
	mode := r.FormValue("mode")
	if mode != "daily" && mode != "weekly" {
		mode = "instant"
	}
	split := func(s string) []string {
		var out []string
		for _, x := range strings.Split(s, ",") {
			if x = strings.TrimSpace(x); x != "" {
				out = append(out, x)
			}
		}
		return out
	}
	hour := 8
	f := notifier.Filter{
		Services: split(r.FormValue("services")), Owners: split(r.FormValue("owners")),
		Environments: split(r.FormValue("envs")), MinJump: versions.Jump(r.FormValue("min_jump")), DigestHour: &hour,
	}
	raw, _ := json.Marshal(f)
	if _, err := s.store.CreateRule(ctx, store.Rule{Scope: p.Scope, ChannelID: ch.ID, EventTypes: r.Form["events"], Filter: raw, Mode: mode}); err != nil {
		return err
	}
	return back(w, r, "/notifications", "notice", "Rule added.")
}

// ---- users and tokens ----

func (s *Server) settingsView(ctx context.Context, p auth.Principal) (SettingsView, error) {
	v := SettingsView{Base: s.base(ctx, p, "settings", "Users")}
	users, err := s.store.ListUsers(ctx, p.User.OrgID)
	if err != nil {
		return v, err
	}
	for _, u := range users {
		v.Users = append(v.Users, UserView{ID: u.ID, Email: u.Email, Role: u.Role, LastLogin: u.LastLoginAt, IsSelf: u.ID == p.User.ID})
	}
	return v, nil
}

func (s *Server) settings(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	v, err := s.settingsView(r.Context(), p)
	if err != nil {
		return err
	}
	v.Base = withFlash(v.Base, r)
	return render(w, r, SettingsPage(v))
}

func (s *Server) showSecret(w http.ResponseWriter, r *http.Request, p auth.Principal, label, secret, notice string) error {
	v, err := s.settingsView(r.Context(), p)
	if err != nil {
		return err
	}
	v.Secret, v.SecretLabel, v.Notice = secret, label, notice
	w.Header().Set("Cache-Control", "no-store")
	return render(w, r, SettingsPage(v))
}

func (s *Server) inviteUser(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	ctx := r.Context()
	role := r.FormValue("role")
	if role == store.RoleOwner && !p.Can(store.RoleOwner) || store.RoleRank(role) == 0 {
		return back(w, r, "/settings", "error", "Choose a role you are allowed to give.")
	}
	email := strings.TrimSpace(r.FormValue("email"))
	if !strings.Contains(email, "@") {
		return back(w, r, "/settings", "error", "Enter an e-mail address.")
	}
	u, err := s.store.CreateUser(ctx, p.User.OrgID, email, "", role)
	if errors.Is(err, store.ErrExists) {
		return back(w, r, "/settings", "error", email+" already has an account.")
	}
	if err != nil {
		return err
	}
	link, err := s.auth.LoginLink(ctx, u)
	if err != nil {
		return err
	}
	return s.showSecret(w, r, p, "Sign-in link for "+u.Email+":", link, u.Email+" invited as "+role+". They can also sign in with an e-mailed link or single sign-on.")
}

func (s *Server) userLink(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	u, err := s.store.GetUser(r.Context(), r.PathValue("id"))
	if err != nil || u.OrgID != p.User.OrgID {
		return back(w, r, "/settings", "error", "Unknown user.")
	}
	link, err := s.auth.LoginLink(r.Context(), u)
	if err != nil {
		return err
	}
	return s.showSecret(w, r, p, "Sign-in link for "+u.Email+":", link, "")
}

func (s *Server) setRole(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	role := r.FormValue("role")
	if store.RoleRank(role) == 0 || (role == store.RoleOwner && !p.Can(store.RoleOwner)) {
		return back(w, r, "/settings", "error", "Choose a role you are allowed to give.")
	}
	u, err := s.store.GetUser(r.Context(), r.PathValue("id"))
	if err != nil || u.OrgID != p.User.OrgID || u.ID == p.User.ID {
		return back(w, r, "/settings", "error", "You cannot change this user.")
	}
	if u.Role == store.RoleOwner && !p.Can(store.RoleOwner) {
		return back(w, r, "/settings", "error", "Only owners can change an owner.")
	}
	if err := s.store.SetUserRole(r.Context(), p.User.OrgID, u.ID, role); err != nil {
		return err
	}
	return back(w, r, "/settings", "notice", u.Email+" is now "+role+".")
}

func (s *Server) deleteUser(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	u, err := s.store.GetUser(r.Context(), r.PathValue("id"))
	if err != nil || u.OrgID != p.User.OrgID || u.ID == p.User.ID {
		return back(w, r, "/settings", "error", "You cannot remove this user.")
	}
	if u.Role == store.RoleOwner && !p.Can(store.RoleOwner) {
		return back(w, r, "/settings", "error", "Only owners can remove an owner.")
	}
	if err := s.store.DeleteUser(r.Context(), p.User.OrgID, u.ID); err != nil {
		return err
	}
	return back(w, r, "/settings", "notice", u.Email+" removed.")
}

func (s *Server) createToken(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" {
		return back(w, r, "/settings", "error", "Give the token a name.")
	}
	token, hash := tokens.New(tokens.API)
	if _, err := s.store.CreateAPIToken(r.Context(), p.Scope, name, hash); err != nil {
		return err
	}
	return s.showSecret(w, r, p, "API token "+name+":", token, "")
}

// Hub fans out "something changed" to open browser streams, per workspace.
type Hub struct {
	mu   sync.Mutex
	subs map[string]map[chan struct{}]bool
}

// NewHub returns an empty hub.
func NewHub() *Hub { return &Hub{subs: map[string]map[chan struct{}]bool{}} }

// Subscribe returns a channel that receives a value after changes in the workspace.
func (h *Hub) Subscribe(workspaceID string) (<-chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	h.mu.Lock()
	if h.subs[workspaceID] == nil {
		h.subs[workspaceID] = map[chan struct{}]bool{}
	}
	h.subs[workspaceID][ch] = true
	h.mu.Unlock()
	return ch, func() {
		h.mu.Lock()
		delete(h.subs[workspaceID], ch)
		h.mu.Unlock()
	}
}

// Publish tells the workspace's streams that data changed. It never blocks.
func (h *Hub) Publish(workspaceID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.subs[workspaceID] {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}
