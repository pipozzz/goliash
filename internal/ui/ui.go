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
	"net"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/a-h/templ"

	"github.com/pipozzz/goliash/internal/api"
	"github.com/pipozzz/goliash/internal/auth"
	"github.com/pipozzz/goliash/internal/mapping"
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

	favMu    sync.Mutex
	favicons map[string]cachedFavicon // per workspace
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
	mux.HandleFunc("GET /sw.js", s.serviceWorker)
	mux.HandleFunc("GET /badge/{ws}/{file}", s.badge)
	mux.HandleFunc("GET /manifest.webmanifest", s.manifest)
	mux.HandleFunc("GET /login", s.login)
	mux.HandleFunc("GET /login/2fa", s.secondFactorPage)
	mux.HandleFunc("GET /setup", s.setupPage)
	mux.HandleFunc("/", s.notFound)

	v, m, a := store.RoleViewer, store.RoleMember, store.RoleAdmin
	mux.Handle("GET /{$}", s.page(v, s.matrix))
	mux.Handle("GET /ui/matrix", s.page(v, s.matrixGrid))
	mux.Handle("GET /ui/stream", s.page(v, s.stream))
	mux.Handle("GET /services/{name}", s.page(v, s.service))
	mux.Handle("POST /services/{name}/policy", s.page(m, s.savePolicy))
	mux.Handle("POST /services/{name}/ack", s.page(m, s.ack))
	mux.Handle("POST /services/{name}/check", s.page(m, s.checkNow))
	mux.Handle("GET /events", s.page(v, s.events))
	mux.Handle("GET /promotions", s.page(v, s.promotions))
	mux.Handle("GET /delivery", s.page(v, s.promotions))
	mux.Handle("GET /report", s.page(v, s.report))
	mux.Handle("GET /hygiene", s.page(v, s.hygiene))
	mux.Handle("GET /updates", s.page(v, s.updates))
	mux.Handle("GET /tiles", s.page(v, s.tiles))
	mux.Handle("GET /apps", s.page(v, s.apps))
	mux.Handle("POST /apps/rename", s.page(m, s.renameApp))
	mux.Handle("POST /apps/team", s.page(m, s.appTeam))
	mux.Handle("GET /teams", s.page(v, s.teams))
	mux.Handle("POST /teams/rename", s.page(m, s.renameTeam))
	mux.Handle("POST /teams/assign", s.page(m, s.assignTeam))
	mux.Handle("GET /ui/favicon.svg", s.page(v, s.favicon))
	mux.Handle("GET /ui/palette.json", s.page(v, s.palette))
	mux.Handle("GET /inbox", s.page(v, s.inbox))
	mux.Handle("POST /inbox/map", s.page(m, s.inboxMap))
	mux.Handle("POST /inbox/ignore", s.page(m, s.inboxIgnore))
	mux.Handle("GET /agents", s.page(v, s.agents))
	mux.Handle("POST /agents", s.page(a, s.createAgent))
	mux.Handle("GET /agents/{id}", s.page(v, s.agent))
	mux.Handle("GET /connect", s.page(a, s.connect))
	mux.Handle("GET /connect/{platform}", s.page(a, s.connectForm))
	mux.Handle("POST /connect/{platform}", s.page(a, s.connectCreate))
	mux.Handle("GET /connect/status/{id}", s.page(v, s.connectProgress))
	mux.Handle("POST /agents/{id}/rotate", s.page(a, s.rotateAgent))
	mux.Handle("POST /agents/{id}/revoke", s.page(a, s.revokeAgent))
	mux.Handle("POST /agents/{id}/rename", s.page(a, s.renameAgent))
	mux.Handle("POST /agents/{id}/delete", s.page(a, s.deleteAgent))
	mux.Handle("POST /targets/{id}/agent", s.page(a, s.moveTarget))
	mux.Handle("POST /targets/{id}/delete", s.page(a, s.deleteTarget))
	mux.Handle("POST /environments", s.page(a, s.createEnvironment))
	mux.Handle("POST /targets", s.page(a, s.createTarget))
	mux.Handle("GET /notifications", s.page(v, s.notifications))
	mux.Handle("POST /notifications/channels", s.page(a, s.createChannel))
	mux.Handle("POST /notifications/channels/{id}/test", s.page(a, s.testChannel))
	mux.Handle("POST /notifications/channels/{id}/push", s.page(v, s.pushSubscribe))
	mux.Handle("POST /notifications/channels/{id}/push/delete", s.page(v, s.pushUnsubscribe))
	mux.Handle("POST /ui/push/status", s.page(v, s.pushStatus))
	mux.Handle("POST /notifications/rules", s.page(m, s.createRule))
	mux.Handle("POST /notifications/channels/{id}/delete", s.page(a, s.deleteChannel))
	mux.Handle("GET /notifications/channels/{id}", s.page(a, s.editChannel))
	mux.Handle("POST /notifications/channels/{id}", s.page(a, s.updateChannel))
	mux.Handle("POST /services/{name}/delete", s.page(a, s.deleteService))
	mux.Handle("POST /workspaces/{id}/rename", s.page(a, s.renameWorkspace))
	mux.Handle("POST /notifications/rules/{id}/pause", s.page(m, s.pauseRule))
	mux.Handle("POST /notifications/rules/{id}/plan", s.page(m, s.sendPlanNow))
	mux.Handle("POST /notifications/deliveries/{id}/retry", s.page(m, s.retryDelivery))
	mux.Handle("POST /notifications/rules/{id}/delete", s.page(m, s.deleteRule))
	mux.Handle("GET /notifications/rules/{id}", s.page(m, s.editRule))
	mux.Handle("POST /notifications/rules/{id}", s.page(m, s.updateRule))
	mux.Handle("POST /environments/{id}", s.page(a, s.updateEnvironment))
	mux.Handle("POST /environments/{id}/delete", s.page(a, s.deleteEnvironment))
	mux.Handle("GET /targets/{id}", s.page(v, s.targetView))
	mux.Handle("GET /targets/{id}/edit", s.page(a, s.editTarget))
	mux.Handle("POST /targets/{id}", s.page(a, s.updateTarget))
	mux.Handle("POST /inbox/rules/{id}/delete", s.page(m, s.deleteMappingRule))
	mux.Handle("POST /services/{name}/acks/{id}/delete", s.page(m, s.deleteAck))
	mux.Handle("GET /settings", s.page(a, s.settings))
	mux.Handle("POST /settings/users", s.page(a, s.inviteUser))
	mux.Handle("POST /settings/users/{id}/role", s.page(a, s.setRole))
	mux.Handle("POST /settings/users/{id}/link", s.page(a, s.userLink))
	mux.Handle("POST /settings/users/{id}/delete", s.page(a, s.deleteUser))
	mux.Handle("POST /settings/users/{id}/sign-out", s.page(a, s.signOutUser))
	mux.Handle("POST /settings/users/{id}/password/delete", s.page(a, s.removePassword))
	mux.Handle("GET /account", s.page(v, s.account))
	mux.Handle("POST /account/2fa/setup", s.page(v, s.startTwoFactor))
	mux.Handle("GET /account/2fa", s.page(v, s.twoFactorSetup))
	mux.Handle("POST /account/2fa/enable", s.page(v, s.enableTwoFactor))
	mux.Handle("POST /account/2fa/codes", s.page(v, s.newRecoveryCodes))
	mux.Handle("POST /account/2fa/disable", s.page(v, s.disableTwoFactor))
	mux.Handle("POST /settings/users/{id}/2fa/delete", s.page(a, s.resetTwoFactor))
	mux.Handle("POST /settings/require-2fa", s.page(a, s.setRequireTwoFactor))
	mux.Handle("POST /settings/app-label", s.page(a, s.setAppLabel))
	mux.Handle("POST /account/name", s.page(v, s.saveName))
	mux.Handle("POST /account/password", s.page(v, s.changePassword))
	mux.Handle("POST /account/sessions/others", s.page(v, s.signOutOthers))
	mux.Handle("POST /account/sessions/{id}/delete", s.page(v, s.signOutSession))
	mux.Handle("POST /settings/tokens", s.page(a, s.createToken))
	mux.Handle("POST /settings/tokens/{id}/revoke", s.page(a, s.revokeToken))
	mux.Handle("POST /workspace", s.page(v, s.switchWorkspace))
	mux.Handle("GET /workspaces", s.page(a, s.workspaces))
	mux.Handle("POST /workspaces", s.page(a, s.createWorkspace))
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
		if ok && p.Via == "session" && p.Scope.WorkspaceID == "" {
			s.problem(w, r, http.StatusForbidden, "No workspace yet", "You have no access to any workspace yet. Ask an admin to invite you to one.", true)
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
		if s.needsTwoFactor(r, p) {
			_ = back(w, r, "/account", "error", "Your organization requires two-factor sign-in. Set it up to continue.")
			return
		}
		if !p.Can(role) {
			s.problem(w, r, http.StatusForbidden, "Not allowed", "Your role ("+p.Role+") does not allow this. Ask an admin of this workspace.", true)
			return
		}
		if err := h(w, r, p); err != nil {
			s.fail(w, r, err)
		}
	})
}

func (s *Server) fail(w http.ResponseWriter, r *http.Request, err error) {
	s.log.ErrorContext(r.Context(), "ui request failed", "path", r.URL.Path, "err", err)
	s.problem(w, r, http.StatusInternalServerError, "Something went wrong", "The request failed. The error is in the server log; try again in a moment.", true)
}

// problem answers with an error page. Parts of a page that htmx loads get a short
// line instead, so a failed refresh does not put a whole page into a panel.
func (s *Server) problem(w http.ResponseWriter, r *http.Request, status int, title, msg string, signedIn bool) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Header.Get("HX-Request") == "true" && r.Header.Get("HX-Boosted") != "true" {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(status)
		_ = errorLine(msg).Render(r.Context(), w)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_ = ErrorPage(ErrorView{Status: status, Title: title, Message: msg, SignedIn: signedIn}).Render(r.Context(), w)
}

// notFound answers paths nothing else serves: a page for people, JSON for API clients.
func (s *Server) notFound(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/api/") || strings.HasPrefix(r.URL.Path, "/agent/") || !strings.Contains(r.Header.Get("Accept"), "text/html") {
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"title":"Not Found","status":404}`))
		return
	}
	_, signedIn, _ := s.auth.Authenticate(r)
	s.problem(w, r, http.StatusNotFound, "Page not found", "Nothing lives at this address. It may have moved, or the thing it showed was deleted.", signedIn)
}

func (s *Server) base(ctx context.Context, p auth.Principal, page, title string) Base {
	b := Base{
		Title: title, Page: page, Email: p.User.Email, Role: p.Role,
		CanMember: p.Can(store.RoleMember), CanAdmin: p.Can(store.RoleAdmin), OrgAdmin: p.OrgWide(),
		Workspace: p.Workspace.Name,
	}
	for _, a := range p.Workspaces {
		b.Workspaces = append(b.Workspaces, WorkspaceOption{ID: a.Workspace.ID, Name: a.Workspace.Name, Current: a.Workspace.ID == p.Scope.WorkspaceID})
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

// audit records a change made in the UI; kv are detail key/value pairs, never secrets.
func (s *Server) audit(ctx context.Context, p auth.Principal, action string, kv ...string) {
	details := map[string]string{}
	for i := 0; i+1 < len(kv); i += 2 {
		details[kv[i]] = kv[i+1]
	}
	if err := s.store.Audit(ctx, store.AuditEntry{
		OrgID: p.Scope.OrgID, WorkspaceID: p.Scope.WorkspaceID,
		Actor: p.Name(), Action: action, Details: details,
	}); err != nil {
		s.log.ErrorContext(ctx, "audit log write failed", "action", action, "err", err)
	}
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
	v := LoginView{
		Sent: r.URL.Query().Get("sent") == "1", Mail: s.auth.MailEnabled(), OIDC: s.auth.OIDCName(),
		Password: s.auth.PasswordsEnabled(), NoAccounts: s.auth.NoAccounts(r.Context()),
	}
	switch r.URL.Query().Get("error") {
	case "password":
		v.Error = "That e-mail and password do not match an account."
	case "throttled":
		v.Error = "Too many attempts. Wait 15 minutes, or sign in with a link."
	case "setup":
		v.Error = "That setup link is used or expired, or someone has an account already."
	case "expired":
		v.Error = "The sign-in step expired or was already used. Start again."
	case "link":
		v.Error = "That sign-in link is used or expired. Ask for a new one."
	case "oidc":
		v.Error = "Single sign-on did not work for this account. Ask an admin to invite you."
	}
	_ = render(w, r, Login(v))
}

// ---- matrix ----

func (s *Server) overview(ctx context.Context, sc store.Scope, groupBy string) (MatrixGrid, error) {
	o, err := versions.LoadOverview(ctx, s.store, sc)
	if err != nil {
		return MatrixGrid{}, err
	}
	agents, err := s.store.ListAgents(ctx, sc)
	if err != nil {
		return MatrixGrid{}, err
	}
	g := buildGrid(o, len(agents), groupBy)
	if len(g.Rows) == 0 {
		if g.Steps, err = s.firstSteps(ctx, sc, g); err != nil {
			return g, err
		}
	}
	return g, nil
}

// firstSteps is the checklist an empty matrix shows, ticked off as the workspace fills.
func (s *Server) firstSteps(ctx context.Context, sc store.Scope, g MatrixGrid) ([]Step, error) {
	envs, err := s.store.ListEnvironments(ctx, sc)
	if err != nil {
		return nil, err
	}
	chans, err := s.store.ListChannels(ctx, sc)
	if err != nil {
		return nil, err
	}
	users, err := s.store.CountUsers(ctx, sc.OrgID)
	if err != nil {
		return nil, err
	}
	data := g.Unmapped > 0 || len(g.Rows) > 0
	steps := []Step{
		{Title: "Connect a cluster or host", Text: "Kubernetes, Docker, Swarm, Nomad, ECS or Compose files: one form creates the environment, the agent and the target, and gives you the command to start the agent.", Href: "/connect", Action: "Connect", Done: g.Targets > 0 && len(envs) > 0},
		{Title: "Get the first report", Text: "Start the agent with the command Connect shows. The matrix fills in within a minute of its first report.", Href: "/agents", Action: "Open agents", Done: data},
		{Title: "Map workloads to services", Text: "Label workloads with goliash.service, or map their images once in the inbox; new workloads follow.", Href: "/inbox", Action: "Open the inbox", Done: len(g.Rows) > 0},
		{Title: "Get notified", Text: "Send new releases and drift to Slack, Discord, Telegram, ntfy, Grafana, a webhook or e-mail.", Href: "/notifications", Action: "Add a channel", Done: len(chans) > 0, Optional: true},
		{Title: "Invite your team", Text: "Viewers read, members map services and acknowledge, admins configure.", Href: "/settings", Action: "Invite people", Done: users > 1, Optional: true},
	}
	return steps, nil
}

func (s *Server) matrix(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	// The tiles may be this browser's home view; ?view=table asks for the table.
	switch view := r.URL.Query().Get("view"); {
	case view == "table":
		http.SetCookie(w, &http.Cookie{ //nolint:gosec // Secure follows the public URL scheme
			Name: "goliash_view", Value: "table", Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode,
			Secure: strings.HasPrefix(s.publicURL, "https://"), MaxAge: 365 * 24 * 3600,
		})
	case view == "" && r.URL.Query().Get("at") == "":
		if c, err := r.Cookie("goliash_view"); err == nil && c.Value == "tiles" {
			return s.tiles(w, r, p)
		}
	}
	v := MatrixView{Base: withFlash(s.base(r.Context(), p, "matrix", "Matrix"), r)}
	if q := r.URL.Query().Get("at"); q != "" {
		at, err := api.ParseAt(q)
		if err != nil {
			return back(w, r, "/", "error", "Enter a date and time like 2026-09-12T14:00.")
		}
		o, err := versions.OverviewAt(r.Context(), s.store, p.Scope, at)
		if err != nil {
			return err
		}
		v.Grid, v.At = buildGrid(o, 0, s.grouping(w, r, matrixGroupCookie)), at.UTC().Format("2006-01-02T15:04")
		return render(w, r, MatrixPage(v))
	}
	g, err := s.overview(r.Context(), p.Scope, s.grouping(w, r, matrixGroupCookie))
	if err != nil {
		return err
	}
	v.Grid = g
	if findings, err := versions.LoadHygiene(r.Context(), s.store, p.Scope); err == nil {
		for _, f := range findings {
			if f.Severity == "warning" {
				v.Hygiene++
			}
		}
	}
	return render(w, r, MatrixPage(v))
}

func (s *Server) matrixGrid(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	g, err := s.overview(r.Context(), p.Scope, r.URL.Query().Get("group"))
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
	v := ServiceView{Base: withFlash(s.base(ctx, p, "matrix", svc.Name), r), Name: svc.Name, Owner: svc.Owner, App: svc.App, Kind: svc.Kind, Upstream: svc.Upstream}
	ref := o.Refs[svc.ID]
	v.RefRepo = ref.Repo
	v.Private = versions.CheckedByAgent(svc, ref.Repo)
	v.PublicRegistry = ref.Repo != "" && versions.IsPublicRegistry(ref.Repo)
	if u, ok := o.Upstreams[svc.ID]; ok {
		if u.HasLatest {
			v.Latest = u.Latest.Raw
		}
		if u.HasLatestAny {
			v.LatestAny = u.LatestAny.Raw
		}
	}
	v.CheckedAt, v.CheckError, _ = s.store.UpstreamStatus(ctx, p.Scope, svc.ID)
	pol, src, _ := versions.PolicyFor(svc, ref.Repo)
	v.PolicyFrom = string(src)
	v.NotesGitHub, v.NotesGitLab, v.NotesChangelog = pol.GitHub, pol.GitLab, pol.Changelog
	switch own, _ := versions.ParsePolicy(svc.VersionPolicy); {
	case pol.GitHub == "" && pol.GitLab == "" && pol.Changelog == "":
	case own.GitHub != "" || own.GitLab != "" || own.Changelog != "":
		v.NotesFrom = "set in the policy"
	case (pol.GitHub != "" && pol.GitHub == versions.LabelGitHub(svc, ref.Repo)) || (pol.GitLab != "" && pol.GitLab == versions.LabelGitLab(svc, ref.Repo)):
		v.NotesFrom = "from the image's source label"
	default:
		v.NotesFrom = "from the catalog"
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
				se.Versions = append(se.Versions, VersionView{Tag: ver.Tag, Resolved: ver.Resolved, Running: ver.Running, Targets: strings.Join(ver.Targets, ", ")})
			}
		}
		for _, d := range o.DriftsAt(svc.ID, e.ID) {
			b := driftBadge(d)
			if d.App != "" {
				b.Label = d.App + ": " + b.Label
			}
			se.Drifts = append(se.Drifts, b)
		}
		v.Envs = append(v.Envs, se)
		if len(se.Versions) > 0 {
			v.Runs = true
		}
	}

	releases, err := s.store.ListReleases(ctx, p.Scope, svc.ID)
	if err != nil {
		return err
	}
	type relv struct {
		v   versions.Version
		rel store.Release
	}
	var parsed []relv
	for _, rel := range releases {
		if pv, ok := versions.ParseVersion(rel.Version); ok {
			parsed = append(parsed, relv{pv, rel})
		}
	}
	sort.Slice(parsed, func(i, j int) bool { return parsed[i].v.Compare(parsed[j].v) > 0 })
	for i := 0; i < len(parsed) && i < 12; i++ {
		v.Releases = append(v.Releases, ReleaseView{Version: parsed[i].rel.Version, Published: parsed[i].rel.PublishedAt, URL: parsed[i].rel.ChangelogURL})
	}

	if deploys, err := s.store.ListEvents(ctx, p.Scope, store.EventFilter{
		ServiceID: svc.ID, Types: []string{"deployed", "version_changed", "removed"}, Limit: 2000,
	}); err == nil {
		envs, eerr := s.store.ListEnvironments(ctx, p.Scope)
		tgts, terr := s.store.ListTargets(ctx, p.Scope)
		if eerr == nil && terr == nil {
			names := map[string]string{}
			for _, t := range tgts {
				names[t.ID] = t.Name
			}
			v.Versions = versionTimeline(deploys, envs, names, time.Now(), 30*24*time.Hour)
		}
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
		av := AckView{ID: a.ID, Kind: a.Kind, Env: o.Envs[a.EnvironmentID].Name, By: a.CreatedBy, Created: a.CreatedAt}
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
	if v.CanMember {
		v.Badges = s.serviceBadges(r.Context(), p.Scope, v.Name, v.Envs)
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
	if app := strings.TrimSpace(r.FormValue("app")); app != svc.App {
		if app != "" && !appName.MatchString(app) {
			return back(w, r, path, "error", "An application name has letters, digits, spaces, dots, dashes or slashes.")
		}
		if err := s.store.SetServiceApp(ctx, p.Scope, svc.ID, app); err != nil {
			return err
		}
		s.audit(ctx, p, "service.app", "service", svc.Name, "app", app)
	}
	if s.checker != nil {
		_ = s.checker.EvaluateDrift(ctx, p.Scope)
	}
	s.hub.Publish(p.Scope.WorkspaceID)
	s.audit(ctx, p, "service.update", "service", svc.Name, "owner", svc.Owner, "kind", svc.Kind, "upstream", svc.Upstream, "policy", string(raw))
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
	// The Updates page puts items off from its own list and comes back to it.
	if ret := r.FormValue("return"); ret == "/updates" || strings.HasPrefix(ret, "/updates?") {
		path = ret
	}
	a := store.Ack{Scope: p.Scope, ServiceID: svc.ID, Kind: r.FormValue("kind"), UntilVersion: strings.TrimSpace(r.FormValue("until_version")), CreatedBy: p.Name()}
	if name := r.FormValue("environment"); name != "" {
		env, err := s.store.GetEnvironmentByName(ctx, p.Scope, name)
		if err != nil {
			return back(w, r, path, "error", "Unknown environment.")
		}
		a.EnvironmentID = env.ID
	}
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
	s.audit(ctx, p, "ack.create", "service", svc.Name, "kind", a.Kind, "until_version", a.UntilVersion, "until", short(a.UntilAt))
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
	if err := s.checker.CheckService(ctx, p.Scope, svc.ID); errors.Is(err, versions.ErrNeedsCredentials) {
		s.hub.Publish(p.Scope.WorkspaceID)
		return back(w, r, path, "notice", "The registry refused it without credentials, so the agents check it with theirs.")
	} else if err != nil {
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
	v := EventsView{Service: q.Get("service"), Env: q.Get("environment"), Type: q.Get("type"), Types: eventTypes, Since: q.Get("since")}
	f := store.EventFilter{Limit: 50}
	if v.Type != "" {
		f.Types = []string{v.Type}
	}
	if d, ok := sinceOptions[v.Since]; ok {
		f.Since = time.Now().Add(-d)
	} else {
		v.Since = ""
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
		for k, val := range map[string]string{"service": v.Service, "environment": v.Env, "type": v.Type, "since": v.Since} {
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

func (s *Server) report(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	ctx := r.Context()
	now := time.Now().UTC()
	month := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC).AddDate(0, -1, 0) // last full month
	if q := r.URL.Query().Get("month"); q != "" {
		m, err := time.Parse("2006-01", q)
		if err != nil {
			return back(w, r, "/report", "error", "Pick a month like 2026-09.")
		}
		month = m
	}
	rep, err := versions.MonthReport(ctx, s.store, p.Scope, month, now)
	if err != nil {
		return err
	}
	v := ReportView{
		Base: s.base(ctx, p, "promotions", "Report "+month.Format("2006-01")), Month: month.Format("January 2006"),
		Generated: now.Format("2 January 2006 15:04 UTC"), Services: rep.Services, Targets: rep.Targets, Deploys: rep.Deploys,
		Prev: month.AddDate(0, -1, 0).Format("2006-01"), PrevLabel: month.AddDate(0, -1, 0).Format("January"),
	}
	v.Workspace = p.Workspace.Name
	if next := month.AddDate(0, 1, 0); !next.After(now) {
		v.Next, v.NextLabel = next.Format("2006-01"), next.Format("January")
	}
	for _, a := range rep.Attention {
		label, text := attention(a.Kind, a.Detail)
		v.Attention = append(v.Attention, AttentionView{
			Service: a.Service, Environment: a.Environment, Kind: a.Kind,
			Label: label, Text: text, Since: a.Since.Format("2 Jan 2006"),
		})
	}
	for _, e := range rep.Envs {
		v.EnvNames = append(v.EnvNames, e.Name)
	}
	for i := 1; i < len(rep.Envs); i++ {
		v.LeadNames = append(v.LeadNames, rep.Envs[i-1].Name+" → "+rep.Envs[i].Name)
	}
	for _, d := range rep.Delivery {
		row := DeliveryRow{Service: d.Service.Name}
		for _, e := range d.Envs {
			row.Envs = append(row.Envs, DeliveryCell{Deploys: e.Deploys})
		}
		for _, lt := range d.LeadTimes {
			lead := "—"
			if lt.Samples > 0 {
				lead = versions.HumanDuration(lt.Median)
			}
			row.Leads = append(row.Leads, lead)
		}
		v.Delivery = append(v.Delivery, row)
	}
	for _, rel := range rep.Releases {
		v.Releases = append(v.Releases, ReportReleaseView{Service: rel.Service, Version: rel.Version, At: rel.At.Format("2 Jan")})
	}
	return render(w, r, ReportPage(v))
}

func (s *Server) hygiene(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	findings, err := versions.LoadHygiene(r.Context(), s.store, p.Scope)
	if err != nil {
		return err
	}
	v := HygieneView{Base: s.base(r.Context(), p, "hygiene", "Image hygiene"), Kind: r.URL.Query().Get("kind"), Total: len(findings)}
	v.Kinds = []HygieneKind{
		{Kind: "moving-tag", Label: "Moving tags", Help: "latest, stable and the like: the version cannot be known", Warn: true},
		{Kind: "retagged", Label: "Tags pushed again", Help: "one tag running as different images", Warn: true},
		{Kind: "untrusted-registry", Label: "Untrusted registries", Help: "outside GOLIASH_ALLOWED_REGISTRIES", Warn: true},
		{Kind: "unpinned", Label: "No digest known", Help: "the tag could be pushed again unnoticed"},
	}
	known := false
	for i := range v.Kinds {
		for _, f := range findings {
			if f.Kind == v.Kinds[i].Kind {
				v.Kinds[i].Count++
			}
		}
		known = known || v.Kinds[i].Kind == v.Kind
	}
	if !known {
		v.Kind = ""
	}
	for _, f := range findings {
		if v.Kind == "" || f.Kind == v.Kind {
			v.Findings = append(v.Findings, f)
		}
	}
	return render(w, r, HygienePage(v))
}

// sinceOptions are the history page's "changed in the last …" periods.
var sinceOptions = map[string]time.Duration{"1h": time.Hour, "6h": 6 * time.Hour, "24h": 24 * time.Hour, "7d": 7 * 24 * time.Hour}

func (s *Server) promotions(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	ctx := r.Context()
	o, err := versions.LoadOverview(ctx, s.store, p.Scope)
	if err != nil {
		return err
	}
	list, err := versions.Promotions(ctx, s.store, p.Scope, o)
	if err != nil {
		return err
	}
	v := PromotionsView{Base: s.base(ctx, p, "promotions", "Delivery")}
	stats, err := versions.DeliveryStats(ctx, s.store, p.Scope, o, 30*24*time.Hour, time.Now())
	if err != nil {
		return err
	}
	for _, e := range o.Matrix.Environments {
		v.EnvNames = append(v.EnvNames, e.Name)
	}
	for i := 1; i < len(o.Matrix.Environments); i++ {
		v.LeadNames = append(v.LeadNames, o.Matrix.Environments[i-1].Name+" → "+o.Matrix.Environments[i].Name)
	}
	for _, d := range stats {
		row := DeliveryRow{Service: d.Service.Name}
		for _, e := range d.Envs {
			row.Envs = append(row.Envs, DeliveryCell{Deploys: e.Deploys, Last: e.LastDeploy})
		}
		for _, lt := range d.LeadTimes {
			lead := "—"
			if lt.Samples > 0 {
				lead = versions.HumanDuration(lt.Median)
			}
			row.Leads = append(row.Leads, lead)
		}
		v.Delivery = append(v.Delivery, row)
	}
	for _, pr := range list {
		pv := PromotionView{Service: pr.Service.Name, App: pr.App, From: pr.From.Name, To: pr.To.Name, Version: pr.Version, Running: pr.Running, Since: pr.Since}
		for _, rel := range pr.Releases {
			pv.Releases = append(pv.Releases, ReleaseView{Version: rel.Version, Published: rel.PublishedAt, URL: rel.ChangelogURL})
		}
		v.Promotions = append(v.Promotions, pv)
	}
	if err := s.deliveryCharts(r, p, &v); err != nil {
		return err
	}
	return render(w, r, PromotionsPage(v))
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
	rules, err := s.store.ListMappingRules(ctx, sc)
	if err != nil {
		return nil, err
	}
	mapper, _ := mapping.New(rules)
	seen := map[string]bool{}
	var items []InboxItem
	for _, i := range active {
		if i.ServiceID != "" || !i.IsMain || seen[i.TargetID+"/"+i.WorkloadID] {
			continue
		}
		ref := versions.ParseImage(i.Image)
		// Ignored images leave the inbox at once, not only from the next snapshot.
		if mapper.Map(mapping.Workload{Name: i.WorkloadName}, i.ContainerName, ref).Ignore {
			continue
		}
		seen[i.TargetID+"/"+i.WorkloadID] = true
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
	v := InboxView{Base: withFlash(s.base(ctx, p, "inbox", "Inbox"), r), Groups: groupInbox(items)}
	svcName := map[string]string{}
	for _, svc := range svcs {
		v.Services = append(v.Services, svc.Name)
		svcName[svc.ID] = svc.Name
	}
	rules, err := s.store.ListMappingRules(ctx, p.Scope)
	if err != nil {
		return err
	}
	labels := map[string]string{"image_repo": "image", "workload_name": "workload name", "label": "label", "ignore": "ignore"}
	for _, rule := range rules {
		v.Rules = append(v.Rules, MappingRuleView{
			ID: rule.ID, Match: labels[rule.MatchType], Pattern: rule.Pattern, Service: svcName[rule.ServiceID], Created: rule.CreatedAt,
		})
	}
	return render(w, r, InboxPage(v))
}

// groupInbox groups inbox items by image repository, largest groups first.
func groupInbox(items []InboxItem) []InboxGroup {
	byRepo := map[string]*InboxGroup{}
	var order []string
	for _, it := range items {
		g := byRepo[it.Repo]
		if g == nil {
			g = &InboxGroup{Repo: it.Repo, Suggested: it.Suggested}
			byRepo[it.Repo] = g
			order = append(order, it.Repo)
		}
		g.Workloads = append(g.Workloads, it)
		if tag := versions.ParseImage(it.Image).Tag; tag != "" && !slices.Contains(g.Tags, tag) {
			g.Tags = append(g.Tags, tag)
		}
		if it.Env != "" && !slices.Contains(g.Envs, it.Env) {
			g.Envs = append(g.Envs, it.Env)
		}
	}
	groups := make([]InboxGroup, 0, len(order))
	for _, repo := range order {
		groups = append(groups, *byRepo[repo])
	}
	sort.SliceStable(groups, func(a, b int) bool {
		if len(groups[a].Workloads) != len(groups[b].Workloads) {
			return len(groups[a].Workloads) > len(groups[b].Workloads)
		}
		return groups[a].Repo < groups[b].Repo
	})
	return groups
}

var serviceName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,99}$`)

// inboxMap maps inbox workloads to a service and adds a rule, so later ones map by
// themselves. By default it maps every workload running the image (an image rule);
// with only=workload it maps one workload and adds a workload-name rule, which wins
// over image rules (the same image running as web and worker).
func (s *Server) inboxMap(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	ctx := r.Context()
	name := strings.TrimSpace(r.FormValue("service"))
	if !serviceName.MatchString(name) {
		return back(w, r, "/inbox", "error", "Service names use letters, digits, dots, dashes and underscores.")
	}
	repo, workloadName := r.FormValue("repo"), r.FormValue("workload_name")
	onlyWorkload := r.FormValue("only") == "workload"
	if repo == "" || (onlyWorkload && workloadName == "") {
		return back(w, r, "/inbox", "error", "Nothing to map.")
	}
	items, err := s.inboxItems(ctx, p.Scope)
	if err != nil {
		return err
	}
	svc, err := s.store.EnsureService(ctx, p.Scope, name)
	if err != nil {
		return err
	}
	rule := store.MappingRule{Scope: p.Scope, Priority: 100, MatchType: "image_repo", Pattern: regexp.QuoteMeta(repo), ServiceID: svc.ID}
	if onlyWorkload {
		rule = store.MappingRule{Scope: p.Scope, Priority: 50, MatchType: "workload_name", Pattern: regexp.QuoteMeta(workloadName), ServiceID: svc.ID}
	}
	if _, err := s.store.CreateMappingRule(ctx, rule); err != nil {
		return err
	}
	mapped := 0
	for _, it := range items {
		if it.Repo != repo || (onlyWorkload && it.Workload != workloadName) {
			continue
		}
		if err := s.store.MapInstances(ctx, p.Scope, it.TargetID, it.WorkloadID, svc.ID); err != nil {
			return err
		}
		mapped++
	}
	if s.checker != nil {
		_ = s.checker.EvaluateDrift(ctx, p.Scope)
	}
	s.hub.Publish(p.Scope.WorkspaceID)
	s.audit(ctx, p, "mapping.create", "service", svc.Name, rule.MatchType, rule.Pattern)
	msg := fmt.Sprintf("Mapped %s to %s. New workloads running %s map to it by themselves.", plural(mapped, "workload", "workloads"), svc.Name, repo)
	if onlyWorkload {
		msg = fmt.Sprintf("Mapped %s to %s. Workloads named %s keep mapping to it.", workloadName, svc.Name, workloadName)
	}
	return back(w, r, "/inbox", "notice", msg)
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
	s.audit(r.Context(), p, "mapping.ignore", "image", repo)
	return back(w, r, "/inbox", "notice", repo+" is ignored.")
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
		v.Agents = append(v.Agents, agentView(a))
		v.Moves = append(v.Moves, AgentOption{ID: a.ID, Name: a.Name})
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
	perEnv := map[string]int{}
	for _, t := range targets {
		perEnv[t.EnvironmentID]++
	}
	for _, e := range envs {
		v.Envs = append(v.Envs, EnvView{ID: e.ID, Name: e.Name, Position: e.Position, Targets: perEnv[e.ID]})
	}
	agentStale := map[string]bool{}
	for _, a := range agents {
		agentStale[a.ID] = !a.StaleSince.IsZero()
	}
	for _, t := range targets {
		by := agentName[t.AgentID]
		if t.AgentID == "" {
			by = "server"
		}
		tv := TargetView{
			ID: t.ID, AgentID: t.AgentID,
			Name: t.Name, Platform: t.Platform, Env: envName[t.EnvironmentID], Agent: by,
			Status: t.CollectorStatus, Error: t.CollectorError, LastSnapshot: t.LastSnapshotAt,
		}
		if versions.StaleTarget(t, agentStale[t.AgentID], time.Now()) {
			tv.Status = "stale"
			tv.Error = "No recent snapshot; the versions shown may be out of date."
		}
		v.Targets = append(v.Targets, tv)
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
	a, err := s.store.CreateAgent(ctx, p.Scope, name, hash)
	if err != nil {
		return back(w, r, "/agents", "error", "Could not create the agent; is the name taken?")
	}
	s.audit(ctx, p, "agent.create", "agent", name)
	return s.showAgent(w, r, p, a.ID, token, false, "Agent "+name+" created. Copy its token now.")
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
	s.audit(r.Context(), p, "environment.create", "environment", name, "position", strconv.Itoa(pos))
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
	case "kubernetes", "ecs", "nomad", "swarm", "docker", "compose":
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
	s.audit(ctx, p, "target.create", "target", name, "platform", platform, "environment", env.Name, "agent", r.FormValue("agent"))
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
	pushAll, pushMine, err := s.store.PushCounts(ctx, p.Scope, p.User.ID)
	if err != nil {
		return err
	}
	chName := map[string]string{}
	for _, c := range chans {
		chName[c.ID] = c.Name
		var cfg struct {
			URL    string   `json:"url"`
			To     []string `json:"to"`
			ChatID string   `json:"chat_id"`
		}
		_ = json.Unmarshal(c.Config, &cfg)
		detail := strings.Join(cfg.To, ", ")
		if cfg.ChatID != "" {
			detail = "chat " + cfg.ChatID
		}
		if cfg.URL != "" {
			if u, err := url.Parse(cfg.URL); err == nil {
				detail = u.Host // never show the full webhook URL: it is a secret
			}
		}
		cv := ChannelView{ID: c.ID, Name: c.Name, Type: c.Type, Detail: detail}
		if c.Type == "push" {
			cv.Browsers, cv.Mine = pushAll[c.ID], pushMine[c.ID]
			cv.Detail = plural(cv.Browsers, "browser", "browsers")
			v.PushKey = s.pushKey(r)
		}
		v.Channels = append(v.Channels, cv)
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
		if len(f.Apps) > 0 {
			parts = append(parts, "applications "+strings.Join(f.Apps, ", "))
		}
		if len(f.Environments) > 0 {
			parts = append(parts, "in "+strings.Join(f.Environments, ", "))
		}
		if f.MinJump != "" {
			parts = append(parts, "releases from "+string(f.MinJump))
		}
		names := make([]string, 0, len(rule.EventTypes))
		for _, t := range rule.EventTypes {
			if l, ok := ruleEventLabels[t]; ok {
				names = append(names, l)
			} else {
				names = append(names, t)
			}
		}
		events := strings.Join(names, ", ")
		if events == "" {
			events = "everything"
		}
		zone := "UTC"
		if f.Timezone != "" {
			zone = f.Timezone
		}
		mode := rule.Mode
		if mode != "instant" && f.DigestHour != nil {
			mode += fmt.Sprintf(" at %02d:00 %s", *f.DigestHour, zone)
		}
		if mode == "instant" && f.Quiet() {
			mode += fmt.Sprintf(", quiet %02d:00–%02d:00 %s", *f.QuietFrom, *f.QuietTo, zone)
		}
		v.Rules = append(v.Rules, RuleView{
			ID: rule.ID, Paused: rule.Paused, Channel: chName[rule.ChannelID], Events: events, Mode: mode,
			Plan:   slices.Contains(rule.EventTypes, notifier.EventUpdatesPlan),
			Filter: orDash(strings.Join(parts, "; ")),
		})
	}
	deliveries, err := s.store.RecentDeliveries(ctx, p.Scope, 25)
	if err != nil {
		return err
	}
	for _, d := range deliveries {
		v.Deliveries = append(v.Deliveries, deliveryView(d, time.Now().UTC()))
	}
	return render(w, r, NotificationsPage(v))
}

func (s *Server) createChannel(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	name := strings.TrimSpace(r.FormValue("name"))
	typ := r.FormValue("type")
	cfg, problem := channelConfig(r, typ, nil)
	if problem != "" {
		return back(w, r, "/notifications", "error", problem)
	}
	if name == "" {
		return back(w, r, "/notifications", "error", "Give the channel a name.")
	}
	if typ == "email" && !s.smtp && cfg["smtp_addr"] == nil {
		return back(w, r, "/notifications", "error", "This server has no mail relay: give the channel its own mail server.")
	}
	raw, _ := json.Marshal(cfg)
	if _, err := s.store.CreateChannel(r.Context(), store.Channel{Scope: p.Scope, Type: typ, Name: name, Config: raw}); err != nil {
		return back(w, r, "/notifications", "error", "Could not add the channel; is the name taken?")
	}
	s.audit(r.Context(), p, "channel.create", "channel", name, "type", typ)
	return back(w, r, "/notifications", "notice", "Channel "+name+" added. Send a test to check it.")
}

// channelConfig reads a channel's settings from a form. When editing, old holds the
// stored settings: an empty field keeps its value, so secrets never travel back to
// the browser. problem is a message for the person when something is missing.
func channelConfig(r *http.Request, typ string, old map[string]any) (cfg map[string]any, problem string) {
	field := func(form, key string) string {
		if v := strings.TrimSpace(r.FormValue(form)); v != "" {
			return v
		}
		if v, ok := old[key].(string); ok {
			return v
		}
		return ""
	}
	httpURL := func(raw string) (string, bool) {
		u, err := url.Parse(raw)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
			return "", false
		}
		return u.String(), true
	}
	cfg = map[string]any{}
	switch typ {
	case "slack", "webhook", "discord", "ntfy", "teams", "gchat":
		u, ok := httpURL(field("url", "url"))
		if !ok {
			return nil, "Enter the full URL, starting with https://."
		}
		cfg["url"] = u
		if secret := field("secret", "secret"); secret != "" && typ == "webhook" {
			cfg["secret"] = secret
		}
		if token := field("token", "token"); token != "" && typ == "ntfy" {
			cfg["token"] = token
		}
	case "grafana":
		u, ok := httpURL(field("url", "url"))
		token := field("token", "token")
		if !ok || token == "" {
			return nil, "Enter Grafana's URL and a service account token."
		}
		cfg["url"], cfg["token"] = u, token
	case "telegram":
		token, chat := field("token", "bot_token"), field("chat_id", "chat_id")
		if token == "" || chat == "" {
			return nil, "Enter the bot token and the chat ID."
		}
		cfg["bot_token"], cfg["chat_id"] = token, chat
	case "push":
		// Nothing to set: browsers subscribe from the Notifications page.
	case "email":
		var to []string
		for _, a := range strings.Split(r.FormValue("to"), ",") {
			if a = strings.TrimSpace(a); a != "" {
				to = append(to, a)
			}
		}
		if len(to) == 0 {
			return nil, "Enter at least one recipient."
		}
		for _, a := range to {
			if !strings.Contains(a, "@") || strings.ContainsAny(a, "\r\n<>") {
				return nil, a + " is not an e-mail address."
			}
		}
		cfg["to"] = to
		// The channel's own mail server, optional: the server's GOLIASH_SMTP_* otherwise.
		if r.FormValue("smtp_clear") != "" {
			break
		}
		addr := field("smtp_addr", "smtp_addr")
		if addr == "" {
			break
		}
		if _, port, err := net.SplitHostPort(addr); err != nil || port == "" {
			return nil, "Give the mail server as host:port, such as smtp.example.com:587."
		}
		from := field("smtp_from", "smtp_from")
		if !strings.Contains(from, "@") || strings.ContainsAny(from, "\r\n") {
			return nil, "Give the address mail comes from, such as goliash@example.com."
		}
		cfg["smtp_addr"], cfg["smtp_from"] = addr, from
		if u := field("smtp_username", "smtp_username"); u != "" {
			cfg["smtp_username"] = u
			if pw := field("smtp_password", "smtp_password"); pw != "" {
				cfg["smtp_password"] = pw
			}
		}
		switch mode := r.FormValue("smtp_tls"); mode {
		case "tls", "starttls", "none":
			cfg["smtp_tls"] = mode
		case "":
			if v, ok := old["smtp_tls"].(string); ok {
				cfg["smtp_tls"] = v
			}
		default:
			return nil, "Unknown TLS mode."
		}
	default:
		return nil, "Unknown channel type."
	}
	return cfg, ""
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

// sendPlanNow sends a rule's upgrade plan at once, to check it without waiting.
func (s *Server) sendPlanNow(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	n, err := s.notify.SendPlanNow(r.Context(), p.Scope, r.PathValue("id"))
	switch {
	case errors.Is(err, store.ErrNotFound):
		return back(w, r, "/notifications", "error", "That rule is gone.")
	case errors.Is(err, notifier.ErrNotAPlanRule):
		return back(w, r, "/notifications", "error", "That rule does not send the upgrade plan.")
	case err != nil:
		return back(w, r, "/notifications", "error", "Sending the plan failed: "+err.Error())
	case n == 0:
		return back(w, r, "/notifications", "notice", "Nothing to upgrade for this rule's filters, so nothing was sent.")
	}
	s.audit(r.Context(), p, "rule.plan_now", "rule", r.PathValue("id"), "updates", itoa(n))
	return back(w, r, "/notifications", "notice", "Upgrade plan sent: "+plural(n, "update", "updates")+".")
}

// ruleFromForm reads a rule's settings from the add or edit form. problem is a message
// for the person when something is off.
func (s *Server) ruleFromForm(r *http.Request, p auth.Principal) (rule store.Rule, channel, problem string) {
	ctx := r.Context()
	if err := r.ParseForm(); err != nil {
		return rule, "", "The form did not arrive whole."
	}
	ch, err := s.store.GetChannelByName(ctx, p.Scope, r.FormValue("channel"))
	if err != nil {
		return rule, "", "Unknown channel."
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
	hour, err := strconv.Atoi(r.FormValue("digest_hour"))
	if err != nil || hour < 0 || hour > 23 {
		hour = 8
	}
	f := notifier.Filter{
		Services: split(r.FormValue("services")), Owners: split(r.FormValue("owners")),
		Environments: split(r.FormValue("envs")), MinJump: versions.Jump(r.FormValue("min_jump")), DigestHour: &hour,
		Apps: split(r.FormValue("apps")),
	}
	if tz := strings.TrimSpace(r.FormValue("timezone")); tz != "" && tz != "UTC" {
		if _, err := time.LoadLocation(tz); err != nil {
			return rule, "", "Unknown time zone " + tz + "; use a name such as Europe/Bratislava."
		}
		f.Timezone = tz
	}
	from, errFrom := strconv.Atoi(r.FormValue("quiet_from"))
	to, errTo := strconv.Atoi(r.FormValue("quiet_to"))
	if errFrom == nil && errTo == nil && from >= 0 && from < 24 && to >= 0 && to < 24 && from != to {
		f.QuietFrom, f.QuietTo = &from, &to
	}
	raw, _ := json.Marshal(f)
	events := r.Form["events"]
	if len(events) == 0 {
		return rule, "", "Pick at least one kind of event."
	}
	return store.Rule{Scope: p.Scope, ChannelID: ch.ID, EventTypes: events, Filter: raw, Mode: mode}, ch.Name, ""
}

func (s *Server) createRule(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	rule, channel, problem := s.ruleFromForm(r, p)
	if problem != "" {
		return back(w, r, "/notifications", "error", problem)
	}
	if _, err := s.store.CreateRule(r.Context(), rule); err != nil {
		return err
	}
	s.audit(r.Context(), p, "notification_rule.create", "channel", channel, "mode", rule.Mode, "events", strings.Join(rule.EventTypes, ","))
	return back(w, r, "/notifications", "notice", "Rule added.")
}

// editRule shows a rule's form, filled in.
func (s *Server) editRule(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	ctx := r.Context()
	rules, err := s.store.ListRules(ctx, p.Scope)
	if err != nil {
		return err
	}
	chans, err := s.store.ListChannels(ctx, p.Scope)
	if err != nil {
		return err
	}
	for _, rule := range rules {
		if rule.ID != r.PathValue("id") {
			continue
		}
		v := RuleEditView{Base: withFlash(s.base(ctx, p, "notifications", "Edit rule"), r), ID: rule.ID, Form: ruleForm(rule)}
		for _, c := range chans {
			v.Channels = append(v.Channels, ChannelView{ID: c.ID, Name: c.Name, Type: c.Type})
			if c.ID == rule.ChannelID {
				v.Form.Channel = c.Name
			}
		}
		return render(w, r, RulePage(v))
	}
	return back(w, r, "/notifications", "error", "That rule is gone.")
}

// updateRule saves an edited rule.
func (s *Server) updateRule(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	rule, channel, problem := s.ruleFromForm(r, p)
	if problem != "" {
		return back(w, r, "/notifications/rules/"+r.PathValue("id"), "error", problem)
	}
	rule.ID = r.PathValue("id")
	if err := s.store.UpdateRule(r.Context(), rule); errors.Is(err, store.ErrNotFound) {
		return back(w, r, "/notifications", "error", "That rule is gone.")
	} else if err != nil {
		return err
	}
	s.audit(r.Context(), p, "notification_rule.update", "rule", rule.ID, "channel", channel, "mode", rule.Mode, "events", strings.Join(rule.EventTypes, ","))
	return back(w, r, "/notifications", "notice", "Rule saved.")
}

// ruleForm fills the rule form from a stored rule.
func ruleForm(rule store.Rule) RuleForm {
	var f notifier.Filter
	_ = json.Unmarshal(rule.Filter, &f)
	out := RuleForm{
		Mode: rule.Mode, MinJump: string(f.MinJump), Events: rule.EventTypes, DigestHour: 8,
		Services: strings.Join(f.Services, ", "), Owners: strings.Join(f.Owners, ", "), Envs: strings.Join(f.Environments, ", "),
		Apps: strings.Join(f.Apps, ", "),
	}
	if f.DigestHour != nil {
		out.DigestHour = *f.DigestHour
	}
	out.Timezone, out.QuietFrom, out.QuietTo = f.Timezone, -1, -1
	if f.Quiet() {
		out.QuietFrom, out.QuietTo = *f.QuietFrom, *f.QuietTo
	}
	if len(out.Events) == 0 { // every type
		out.Events = []string{"new_release", "drift_detected", "drift_resolved", "version_changed", "removed", "agent_stale"}
	}
	return out
}

// ---- users, workspaces and tokens ----

// accessLabel describes a user's access to the current workspace.
func orgRoleOf(u store.User) string {
	if store.OrgWide(u.Role) {
		return u.Role
	}
	return ""
}

func accessLabel(u store.User, wsRole string) string {
	if store.OrgWide(u.Role) {
		return u.Role + " of the organization"
	}
	if wsRole == "" {
		return "no access here"
	}
	return wsRole
}

func (s *Server) settingsView(ctx context.Context, p auth.Principal) (SettingsView, error) {
	v := SettingsView{Base: s.base(ctx, p, "settings", "Users"), CanGrantOwner: p.User.Role == store.RoleOwner, SelfHas2FA: p.User.TOTPEnabled}
	if req, err := s.store.RequireTwoFactor(ctx, p.User.OrgID); err == nil {
		v.Require2FA = req
	}
	if key, err := s.store.WorkspaceAppLabel(ctx, p.Scope.WorkspaceID); err == nil {
		v.AppLabel = key
	}
	users, err := s.store.ListUsers(ctx, p.User.OrgID)
	if err != nil {
		return v, err
	}
	roles, err := s.store.WorkspaceRoles(ctx, p.Scope.WorkspaceID)
	if err != nil {
		return v, err
	}
	for _, u := range users {
		// Workspace admins see the people of their workspace only (an MSP's clients
		// never see each other); organization admins see everyone.
		if !p.OrgWide() && roles[u.ID] == "" {
			continue
		}
		v.Users = append(v.Users, UserView{
			ID: u.ID, Email: u.Email, Role: roles[u.ID], Access: accessLabel(u, roles[u.ID]), OrgWide: store.OrgWide(u.Role),
			OrgRole:   orgRoleOf(u),
			LastLogin: u.LastLoginAt, IsSelf: u.ID == p.User.ID, HasPassword: u.HasPassword, TOTP: u.TOTPEnabled,
		})
	}
	toks, err := s.store.ListAPITokens(ctx, p.Scope)
	if err != nil {
		return v, err
	}
	now := time.Now()
	for _, t := range toks {
		v.Tokens = append(v.Tokens, TokenView{
			ID: t.ID, Name: t.Name, Role: t.Role, CreatedBy: t.CreatedBy, CreatedAt: t.CreatedAt,
			LastUsed: t.LastUsed, ExpiresAt: t.ExpiresAt, Expired: t.Expired(now),
		})
	}
	entries, err := s.store.ListAudit(ctx, p.User.OrgID, 200)
	if err != nil {
		return v, err
	}
	for _, e := range entries {
		if !p.OrgWide() && e.WorkspaceID != p.Scope.WorkspaceID {
			continue
		}
		keys := make([]string, 0, len(e.Details))
		for k := range e.Details {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		var parts []string
		for _, k := range keys {
			if e.Details[k] != "" && e.Details[k] != "never" {
				parts = append(parts, k+"="+e.Details[k])
			}
		}
		v.Audit = append(v.Audit, AuditView{At: e.At, Actor: e.Actor, Action: e.Action, Details: strings.Join(parts, " ")})
		if len(v.Audit) == 100 {
			break
		}
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

// inviteUser gives an e-mail address access: a role in this workspace (viewer,
// member, admin), or the whole organization (org-admin, owner) for organization admins.
func (s *Server) inviteUser(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	ctx := r.Context()
	access := r.FormValue("role")
	email := strings.TrimSpace(r.FormValue("email"))
	if !strings.Contains(email, "@") {
		return back(w, r, "/settings", "error", "Enter an e-mail address.")
	}
	orgRole := ""
	switch access {
	case store.RoleViewer, store.RoleMember, store.RoleAdmin:
	case "org-admin":
		orgRole = store.RoleAdmin
	case store.RoleOwner:
		orgRole = store.RoleOwner
	default:
		return back(w, r, "/settings", "error", "Choose a role.")
	}
	if (orgRole != "" && !p.OrgWide()) || (orgRole == store.RoleOwner && p.User.Role != store.RoleOwner) {
		return back(w, r, "/settings", "error", "Choose a role you are allowed to give.")
	}

	userRole := orgRole
	if userRole == "" {
		userRole = store.RoleViewer // workspace access comes from the membership
		if access != store.RoleViewer {
			userRole = store.RoleMember
		}
	}
	u, err := s.store.CreateUser(ctx, p.User.OrgID, email, "", userRole)
	isNew := err == nil
	if errors.Is(err, store.ErrExists) {
		if u, err = s.store.GetUserByEmail(ctx, p.User.OrgID, email); err != nil {
			return err
		}
		if orgRole != "" || store.OrgWide(u.Role) {
			return back(w, r, "/settings", "error", email+" already has an account.")
		}
	} else if err != nil {
		return err
	}
	if orgRole == "" {
		if err := s.store.SetMembership(ctx, u.ID, p.Scope.WorkspaceID, access); err != nil {
			return err
		}
	}
	s.audit(ctx, p, "user.invite", "user", u.Email, "role", access, "workspace", p.Workspace.Slug)
	if !isNew {
		return back(w, r, "/settings", "notice", u.Email+" now has "+access+" access to "+p.Workspace.Name+".")
	}
	link, err := s.auth.LoginLink(ctx, u)
	if err != nil {
		return err
	}
	return s.showSecret(w, r, p, "Sign-in link for "+u.Email+":", link,
		u.Email+" invited ("+access+"). They can also sign in with an e-mailed link or single sign-on.")
}

// manageable returns a user the principal may manage in the current workspace.
func (s *Server) manageable(ctx context.Context, p auth.Principal, id string) (store.User, string, bool) {
	u, err := s.store.GetUser(ctx, id)
	if err != nil || u.OrgID != p.User.OrgID || u.ID == p.User.ID {
		return store.User{}, "", false
	}
	roles, err := s.store.WorkspaceRoles(ctx, p.Scope.WorkspaceID)
	if err != nil {
		return store.User{}, "", false
	}
	if store.OrgWide(u.Role) && !p.OrgWide() {
		return store.User{}, "", false
	}
	if !p.OrgWide() && roles[u.ID] == "" {
		return store.User{}, "", false
	}
	return u, roles[u.ID], true
}

func (s *Server) userLink(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	u, _, ok := s.manageable(r.Context(), p, r.PathValue("id"))
	if !ok {
		return back(w, r, "/settings", "error", "Unknown user.")
	}
	link, err := s.auth.LoginLink(r.Context(), u)
	if err != nil {
		return err
	}
	s.audit(r.Context(), p, "user.login_link", "user", u.Email)
	return s.showSecret(w, r, p, "Sign-in link for "+u.Email+":", link, "")
}

// setRole changes a person's role in this workspace; "none" takes their access away.
func (s *Server) setRole(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	ctx := r.Context()
	role := r.FormValue("role")
	u, old, ok := s.manageable(ctx, p, r.PathValue("id"))
	if !ok {
		return back(w, r, "/settings", "error", "You cannot change this user here.")
	}
	if old == "" && store.OrgWide(u.Role) {
		old = "org-" + u.Role
	}
	orgRole := map[string]string{"org-admin": store.RoleAdmin, store.RoleOwner: store.RoleOwner}[role]
	if orgRole != "" || store.OrgWide(u.Role) {
		// Organization roles: only organization admins change them, only owners touch owners,
		// and the last owner stays.
		if !p.OrgWide() || ((orgRole == store.RoleOwner || u.Role == store.RoleOwner) && p.User.Role != store.RoleOwner) {
			return back(w, r, "/settings", "error", "Choose a role you are allowed to give.")
		}
		if u.Role == store.RoleOwner && orgRole != store.RoleOwner {
			if n, err := s.countOwners(ctx, u.OrgID); err != nil || n < 2 {
				return back(w, r, "/settings", "error", "The organization needs another owner first.")
			}
		}
	}
	if orgRole != "" {
		if err := s.store.SetUserRole(ctx, u.OrgID, u.ID, orgRole); err != nil {
			return err
		}
		s.audit(ctx, p, "user.role", "user", u.Email, "from", old, "to", "org-"+orgRole)
		return back(w, r, "/settings", "notice", u.Email+" is now "+orgRole+" of the organization.")
	}
	if store.OrgWide(u.Role) {
		// Back to workspace access: memberships decide from now on, starting with this workspace.
		base := store.RoleMember
		if role == store.RoleViewer {
			base = store.RoleViewer
		}
		if err := s.store.SetUserRole(ctx, u.OrgID, u.ID, base); err != nil {
			return err
		}
	}
	switch role {
	case store.RoleViewer, store.RoleMember, store.RoleAdmin:
		if err := s.store.SetMembership(ctx, u.ID, p.Scope.WorkspaceID, role); err != nil {
			return err
		}
	case "none":
		if err := s.store.RemoveMembership(ctx, u.ID, p.Scope.WorkspaceID); err != nil {
			return err
		}
	default:
		return back(w, r, "/settings", "error", "Choose a role.")
	}
	s.audit(ctx, p, "user.role", "user", u.Email, "from", old, "to", role, "workspace", p.Workspace.Slug)
	if role == "none" {
		return back(w, r, "/settings", "notice", u.Email+" no longer has access to "+p.Workspace.Name+".")
	}
	return back(w, r, "/settings", "notice", u.Email+" is now "+role+" in "+p.Workspace.Name+".")
}

func (s *Server) countOwners(ctx context.Context, orgID string) (int, error) {
	users, err := s.store.ListUsers(ctx, orgID)
	n := 0
	for _, u := range users {
		if u.Role == store.RoleOwner {
			n++
		}
	}
	return n, err
}

func (s *Server) deleteUser(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	u, _, ok := s.manageable(r.Context(), p, r.PathValue("id"))
	if !ok || !p.OrgWide() {
		return back(w, r, "/settings", "error", "Only organization admins can remove people.")
	}
	if u.Role == store.RoleOwner && p.User.Role != store.RoleOwner {
		return back(w, r, "/settings", "error", "Only owners can remove an owner.")
	}
	if u.Role == store.RoleOwner {
		if n, err := s.countOwners(r.Context(), u.OrgID); err != nil || n < 2 {
			return back(w, r, "/settings", "error", "The organization needs another owner first.")
		}
	}
	if err := s.store.DeleteUser(r.Context(), p.User.OrgID, u.ID); err != nil {
		return err
	}
	s.audit(r.Context(), p, "user.delete", "user", u.Email, "role", u.Role)
	return back(w, r, "/settings", "notice", u.Email+" removed.")
}

func (s *Server) createToken(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" {
		return back(w, r, "/settings", "error", "Give the token a name.")
	}
	if len(name) > 100 {
		return back(w, r, "/settings", "error", "Use at most 100 characters for the name.")
	}
	role := r.FormValue("role")
	if role != store.RoleViewer && role != store.RoleMember {
		return back(w, r, "/settings", "error", "Choose viewer or member.")
	}
	t := store.APIToken{Name: name, Role: role, CreatedBy: p.Name()}
	if days, err := strconv.Atoi(r.FormValue("expires")); err == nil && days > 0 && days <= 3650 {
		t.ExpiresAt = time.Now().Add(time.Duration(days) * 24 * time.Hour)
	}
	token, hash := tokens.New(tokens.API)
	if _, err := s.store.CreateAPIToken(r.Context(), p.Scope, t, hash); err != nil {
		return err
	}
	expires := "never"
	if !t.ExpiresAt.IsZero() {
		expires = t.ExpiresAt.UTC().Format(time.DateOnly)
	}
	s.audit(r.Context(), p, "api_token.create", "token", name, "role", role, "expires", expires)
	return s.showSecret(w, r, p, "API token "+name+" ("+role+") for "+p.Workspace.Name+":", token, "")
}

func (s *Server) revokeToken(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	toks, err := s.store.ListAPITokens(r.Context(), p.Scope)
	if err != nil {
		return err
	}
	for _, t := range toks {
		if t.ID != r.PathValue("id") {
			continue
		}
		if err := s.store.RevokeAPIToken(r.Context(), p.Scope, t.ID); err != nil {
			return err
		}
		s.audit(r.Context(), p, "api_token.revoke", "token", t.Name)
		return back(w, r, "/settings", "notice", "Token "+t.Name+" revoked. It stops working now.")
	}
	return back(w, r, "/settings", "error", "That token is already revoked.")
}

// Cookies that remember how this browser last grouped the matrix and target pages.
const (
	matrixGroupCookie = "goliash_matrix_group"
	targetGroupCookie = "goliash_target_group"
)

// grouping returns the grouping a page asks for: the link's ?group=, remembered in
// cookie for the next visit, else the remembered one. Unknown values fall back later.
func (s *Server) grouping(w http.ResponseWriter, r *http.Request, cookie string) string {
	if g := r.URL.Query().Get("group"); g != "" {
		if len(g) <= 16 {
			http.SetCookie(w, &http.Cookie{ //nolint:gosec // Secure follows the public URL scheme
				Name: cookie, Value: g, Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode,
				Secure: strings.HasPrefix(s.publicURL, "https://"), MaxAge: 365 * 24 * 3600,
			})
		}
		return g
	}
	if c, err := r.Cookie(cookie); err == nil {
		return c.Value
	}
	return ""
}

// switchWorkspace remembers the workspace this browser works in.
func (s *Server) switchWorkspace(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	id := r.FormValue("workspace")
	for _, a := range p.Workspaces {
		if a.Workspace.ID == id {
			http.SetCookie(w, &http.Cookie{ //nolint:gosec // Secure follows the public URL scheme
				Name: auth.WorkspaceCookie, Value: id, Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode,
				Secure: strings.HasPrefix(s.publicURL, "https://"), MaxAge: 365 * 24 * 3600,
			})
			return back(w, r, "/", "notice", "Switched to "+a.Workspace.Name+".")
		}
	}
	return back(w, r, "/", "error", "You cannot open that workspace.")
}

var workspaceSlug = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

func (s *Server) workspaces(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	ctx := r.Context()
	if !p.OrgWide() {
		s.problem(w, r, http.StatusForbidden, "Not allowed", "Only organization owners and admins manage workspaces.", true)
		return nil
	}
	v := WorkspacesView{Base: withFlash(s.base(ctx, p, "workspaces", "Workspaces"), r)}
	all, err := s.store.ListOrgWorkspaces(ctx, p.User.OrgID)
	if err != nil {
		return err
	}
	for _, ws := range all {
		c, err := s.store.CountWorkspace(ctx, ws.ID)
		if err != nil {
			return err
		}
		v.Items = append(v.Items, WorkspaceItem{
			ID: ws.ID, Name: ws.Name, Slug: ws.Slug, Targets: c.Targets,
			Services: c.Services, Members: c.Members, Current: ws.ID == p.Scope.WorkspaceID,
		})
	}
	return render(w, r, WorkspacesPage(v))
}

func (s *Server) createWorkspace(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	ctx := r.Context()
	if !p.OrgWide() {
		s.problem(w, r, http.StatusForbidden, "Not allowed", "Only organization owners and admins manage workspaces.", true)
		return nil
	}
	name := strings.TrimSpace(r.FormValue("name"))
	slug := strings.ToLower(strings.TrimSpace(r.FormValue("slug")))
	if name == "" || !workspaceSlug.MatchString(slug) {
		return back(w, r, "/workspaces", "error", "Give a name and a slug of lowercase letters, digits and dashes.")
	}
	ws, err := s.store.CreateWorkspace(ctx, p.User.OrgID, name, slug)
	if err != nil {
		return back(w, r, "/workspaces", "error", "Could not create the workspace; is the slug taken?")
	}
	for i, env := range []string{"dev", "staging", "prod"} {
		if r.FormValue("envs") == "1" {
			if _, err := s.store.CreateEnvironment(ctx, ws.Scope(), env, (i+1)*10); err != nil {
				return err
			}
		}
	}
	// An organization-level change: recorded without a workspace, so only organization admins see it.
	_ = s.store.Audit(ctx, store.AuditEntry{
		OrgID: p.User.OrgID, Actor: p.Name(), Action: "workspace.create",
		Details: map[string]string{"workspace": ws.Slug, "name": ws.Name},
	})
	return back(w, r, "/workspaces", "notice", "Workspace "+ws.Name+" created. Switch to it to add agents and people.")
}

// Hub fans out "something changed" to open browser streams, per workspace.
type Hub struct {
	mu       sync.Mutex
	subs     map[string]map[chan struct{}]bool
	versions map[string]uint64 // per workspace, counts the changes this server heard of
	relay    func(workspaceID string) error
}

// SetRelay sends changes through relay (to every server, this one included, which
// then calls PublishLocal) instead of only to this server's browsers.
func (h *Hub) SetRelay(relay func(workspaceID string) error) { h.relay = relay }

// NewHub returns an empty hub.
func NewHub() *Hub {
	return &Hub{subs: map[string]map[chan struct{}]bool{}, versions: map[string]uint64{}}
}

// Version is how many changes of the workspace this server heard of: what is
// computed from its data stays valid while it does not move.
func (h *Hub) Version(workspaceID string) uint64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.versions[workspaceID]
}

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
	if h.relay != nil && h.relay(workspaceID) == nil {
		return
	}
	h.PublishLocal(workspaceID)
}

// PublishLocal tells this server's browsers of a workspace that something changed.
func (h *Hub) PublishLocal(workspaceID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.versions[workspaceID]++
	for ch := range h.subs[workspaceID] {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

var labelKey = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9._/-]{0,252})$`)

// setAppLabel sets the label key that names a workload's application in this
// workspace, before the well-known ones. It applies from each target's next snapshot.
func (s *Server) setAppLabel(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	key := strings.TrimSpace(r.FormValue("key"))
	if key != "" && !labelKey.MatchString(key) {
		return back(w, r, "/settings", "error", "A label key is letters, digits, dots, dashes, underscores and slashes, like example.com/app.")
	}
	if err := s.store.SetWorkspaceAppLabel(r.Context(), p.Scope.OrgID, p.Scope.WorkspaceID, key); err != nil {
		return err
	}
	s.audit(r.Context(), p, "workspace.app_label", "key", key)
	if key == "" {
		return back(w, r, "/settings", "notice", "Applications come from the well-known labels again, from each target's next snapshot.")
	}
	return back(w, r, "/settings", "notice", "Applications come from the "+key+" label first, from each target's next snapshot.")
}
