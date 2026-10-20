// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	spec "github.com/pipozzz/goliash/api"
	"github.com/pipozzz/goliash/internal/auth"
	"github.com/pipozzz/goliash/internal/notifier"
	"github.com/pipozzz/goliash/internal/store"
	"github.com/pipozzz/goliash/internal/versions"
	"github.com/pipozzz/goliash/pkg/buildinfo"
)

// PublicHandler serves the REST API under /api/v1 and Prometheus metrics.
type PublicHandler struct {
	store    *store.Store
	auth     *auth.Auth
	log      *slog.Logger
	isLeader func() bool
	// for /api/v1/status
	checker     *versions.Checker
	version     string
	updateCheck bool
}

// SetHealth gives /api/v1/status the checker, the server's release and whether it may look for a newer one.
func (h *PublicHandler) SetHealth(checker *versions.Checker, version string, updateCheck bool) {
	h.checker, h.version, h.updateCheck = checker, version, updateCheck
}

// SetLeader reports, in /metrics, whether this server runs the background work.
func (h *PublicHandler) SetLeader(isLeader func() bool) { h.isLeader = isLeader }

// NewPublicHandler returns the public API handler.
func NewPublicHandler(st *store.Store, a *auth.Auth, log *slog.Logger) *PublicHandler {
	return &PublicHandler{store: st, auth: a, log: log}
}

// Register adds the routes to mux.
func (h *PublicHandler) Register(mux *http.ServeMux) {
	mux.Handle("GET /api/v1/matrix", h.with(store.RoleViewer, h.matrix))
	mux.Handle("GET /api/v1/services", h.with(store.RoleViewer, h.services))
	mux.Handle("GET /api/v1/services/{name}/release-notes", h.with(store.RoleViewer, h.releaseNotes))
	mux.Handle("GET /api/v1/services/{name}/images", h.with(store.RoleViewer, h.serviceImages))
	mux.Handle("GET /api/v1/environments", h.with(store.RoleViewer, h.environments))
	mux.Handle("GET /api/v1/targets", h.with(store.RoleViewer, h.targets))
	mux.Handle("GET /api/v1/events", h.with(store.RoleViewer, h.events))
	mux.Handle("GET /api/v1/drifts", h.with(store.RoleViewer, h.drifts))
	mux.Handle("GET /api/v1/promotions", h.with(store.RoleViewer, h.promotions))
	mux.Handle("GET /api/v1/rollouts", h.with(store.RoleViewer, h.rollouts))
	mux.Handle("GET /api/v1/gitops", h.with(store.RoleViewer, h.gitOps))
	mux.Handle("GET /api/v1/pull-requests", h.with(store.RoleViewer, h.pullRequests))
	mux.Handle("GET /api/v1/delivery", h.with(store.RoleViewer, h.delivery))
	mux.Handle("GET /api/v1/inventory", h.with(store.RoleViewer, h.inventory))
	mux.Handle("GET /api/v1/hygiene", h.with(store.RoleViewer, h.hygiene))
	mux.Handle("GET /api/v1/vulnerabilities", h.with(store.RoleViewer, h.vulnerabilities))
	mux.Handle("GET /api/v1/licenses", h.with(store.RoleViewer, h.licenses))
	mux.Handle("GET /api/v1/status", h.with(store.RoleViewer, h.status))
	mux.Handle("GET /api/v1/updates", h.with(store.RoleViewer, h.updates))
	mux.Handle("GET /api/v1/posture", h.with(store.RoleViewer, h.posture))
	mux.Handle("GET /api/v1/summary", h.with(store.RoleViewer, h.summary))
	mux.Handle("GET /api/v1/notifications/sent", h.with(store.RoleViewer, h.notificationsSent))
	mux.Handle("POST /api/v1/acks", h.with(store.RoleMember, h.createAck))
	mux.Handle("GET /metrics", h.with(store.RoleViewer, h.metrics))
	mux.HandleFunc("GET /api/v1/openapi.yaml", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/yaml")
		w.Header().Set("Access-Control-Allow-Origin", "*") // documentation viewers load it cross-origin
		_, _ = w.Write(spec.PublicV1)
	})
	mux.HandleFunc("/api/", func(w http.ResponseWriter, _ *http.Request) {
		writeProblem(w, http.StatusNotFound, "Not found", "unknown API endpoint")
	})
}

type principalHandler func(w http.ResponseWriter, r *http.Request, p auth.Principal)

func (h *PublicHandler) with(role string, next principalHandler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, ok, err := h.auth.Authenticate(r)
		if err != nil {
			h.fail(w, r, err)
			return
		}
		if !ok {
			writeProblem(w, http.StatusUnauthorized, "Unauthorized", "sign in or send a glsh_api_ bearer token")
			return
		}
		if !p.Can(role) {
			writeProblem(w, http.StatusForbidden, "Forbidden", "this needs the "+role+" role")
			return
		}
		next(w, r, p)
	})
}

func (h *PublicHandler) fail(w http.ResponseWriter, r *http.Request, err error) {
	h.log.ErrorContext(r.Context(), "api request failed", "path", r.URL.Path, "err", err)
	writeProblem(w, http.StatusInternalServerError, "Internal server error", "")
}

// JSON shapes of the public API.
type (
	apiEnvironment struct {
		ID       string `json:"id"`
		Name     string `json:"name"`
		Position int    `json:"position"`
	}
	apiVersion struct {
		Tag     string   `json:"tag"`
		Digest  string   `json:"digest,omitempty"`
		Running int      `json:"running"`
		Targets []string `json:"targets"`
	}
	apiDrift struct {
		Service     string               `json:"service"`
		Environment string               `json:"environment"`
		App         string               `json:"app,omitempty"`
		Kind        string               `json:"kind"`
		Since       time.Time            `json:"since"`
		DaysOpen    float64              `json:"days_open"`
		Detail      versions.DriftDetail `json:"detail"`
	}
	apiCell struct {
		Environment  string       `json:"environment"`
		Versions     []apiVersion `json:"versions"`
		Declared     []apiVersion `json:"declared,omitempty"`
		FromDeclared bool         `json:"from_declared,omitempty"`
		Drifts       []apiDrift   `json:"drifts"`
	}
	apiMatrixRow struct {
		Service  string    `json:"service"`
		Owner    string    `json:"owner,omitempty"`
		Upstream string    `json:"upstream,omitempty"`
		Latest   string    `json:"latest,omitempty"`
		Cells    []apiCell `json:"cells"`
	}
	apiMatrix struct {
		Environments []apiEnvironment `json:"environments"`
		Services     []apiMatrixRow   `json:"services"`
		Unmapped     int              `json:"unmapped"`
	}
)

func (h *PublicHandler) matrix(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	at, ok := atParam(w, r)
	if !ok {
		return
	}
	var o versions.Overview
	var err error
	if at != nil {
		o, err = versions.OverviewAt(r.Context(), h.store, p.Scope, *at)
	} else {
		o, err = versions.LoadOverview(r.Context(), h.store, p.Scope)
	}
	if err != nil {
		h.fail(w, r, err)
		return
	}
	out := apiMatrix{Environments: []apiEnvironment{}, Services: []apiMatrixRow{}, Unmapped: o.Matrix.Unmapped}
	for _, e := range o.Matrix.Environments {
		out.Environments = append(out.Environments, apiEnvironment{ID: e.ID, Name: e.Name, Position: e.Position})
	}
	for _, row := range o.Matrix.Rows {
		ar := apiMatrixRow{Service: row.Service.Name, Owner: row.Service.Owner, Upstream: o.Refs[row.Service.ID].Repo, Cells: []apiCell{}}
		if u, ok := o.Upstreams[row.Service.ID]; ok && u.HasLatest {
			ar.Latest = u.Latest.Raw
		}
		for ei, c := range row.Cells {
			env := o.Matrix.Environments[ei]
			cell := apiCell{Environment: env.Name, Versions: []apiVersion{}, Drifts: []apiDrift{}, FromDeclared: c.FromDeclared}
			for _, v := range c.Versions {
				cell.Versions = append(cell.Versions, apiVersion{Tag: v.Tag, Digest: v.Digest, Running: v.Running, Targets: v.Targets})
			}
			if !c.FromDeclared {
				for _, v := range c.Declared {
					cell.Declared = append(cell.Declared, apiVersion{Tag: v.Tag, Digest: v.Digest, Running: v.Running, Targets: v.Targets})
				}
			}
			for _, d := range o.DriftsAt(row.Service.ID, env.ID) {
				cell.Drifts = append(cell.Drifts, toAPIDrift(o, d))
			}
			ar.Cells = append(ar.Cells, cell)
		}
		out.Services = append(out.Services, ar)
	}
	writeJSON(w, http.StatusOK, out)
}

func toAPIDrift(o versions.Overview, d store.Drift) apiDrift {
	var det versions.DriftDetail
	_ = json.Unmarshal(d.Detail, &det)
	return apiDrift{
		Service: o.Services[d.ServiceID].Name, Environment: o.Envs[d.EnvironmentID].Name, App: d.App, Kind: d.Kind,
		Since: d.Since, DaysOpen: time.Since(d.Since).Hours() / 24, Detail: det,
	}
}

func (h *PublicHandler) services(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	svcs, err := h.store.ListServices(r.Context(), p.Scope)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	type apiService struct {
		ID       string          `json:"id"`
		Name     string          `json:"name"`
		Owner    string          `json:"owner"`
		Kind     string          `json:"kind"`
		Upstream string          `json:"upstream"`
		Policy   json.RawMessage `json:"version_policy"`
	}
	o, err := versions.LoadOverview(r.Context(), h.store, p.Scope)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	out := []apiService{}
	for _, s := range svcs {
		out = append(out, apiService{ID: s.ID, Name: s.Name, Owner: s.Owner, Kind: versions.ServiceKind(s, o.Refs[s.ID].Repo), Upstream: s.Upstream, Policy: s.VersionPolicy})
	}
	writeJSON(w, http.StatusOK, out)
}

// releaseNotes answers what changed between two versions of a service, from its GitHub or GitLab releases.
func (h *PublicHandler) releaseNotes(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	ctx := r.Context()
	svc, err := h.store.GetServiceByName(ctx, p.Scope, r.PathValue("name"))
	if errors.Is(err, store.ErrNotFound) {
		writeProblem(w, http.StatusNotFound, "Not found", "no such service")
		return
	}
	if err != nil {
		h.fail(w, r, err)
		return
	}
	o, err := versions.LoadOverview(ctx, h.store, p.Scope)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	from, to := r.URL.Query().Get("from"), r.URL.Query().Get("to")
	if u, ok := o.Upstreams[svc.ID]; to == "" && ok && u.HasLatest {
		to = u.Latest.Raw
	}
	if to == "" {
		writeProblem(w, http.StatusBadRequest, "Bad request", "no newest release is known yet: name the version in to")
		return
	}
	notes, err := h.checker.ReleaseNotes(ctx, svc, o.Refs[svc.ID].Repo, from, to)
	if errors.Is(err, versions.ErrBadVersion) {
		writeProblem(w, http.StatusBadRequest, "Bad request", "from and to must be versions, e.g. 1.27.2")
		return
	}
	if err != nil {
		writeProblem(w, http.StatusBadGateway, "Release notes unavailable", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Service string `json:"service"`
		From    string `json:"from,omitempty"`
		To      string `json:"to"`
		versions.ReleaseNotes
	}{svc.Name, from, to, notes})
}

func (h *PublicHandler) environments(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	envs, err := h.store.ListEnvironments(r.Context(), p.Scope)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	out := []apiEnvironment{}
	for _, e := range envs {
		out = append(out, apiEnvironment{ID: e.ID, Name: e.Name, Position: e.Position})
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *PublicHandler) targets(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	ts, err := h.store.ListTargets(r.Context(), p.Scope)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	envs, _ := h.store.ListEnvironments(r.Context(), p.Scope)
	envName := map[string]string{}
	for _, e := range envs {
		envName[e.ID] = e.Name
	}
	type apiTarget struct {
		ID              string     `json:"id"`
		Name            string     `json:"name"`
		Platform        string     `json:"platform"`
		Environment     string     `json:"environment"`
		CollectedBy     string     `json:"collected_by"` // agent ID or "server"
		LastSnapshotAt  *time.Time `json:"last_snapshot_at,omitempty"`
		CollectorStatus string     `json:"collector_status,omitempty"`
		CollectorError  string     `json:"collector_error,omitempty"`
	}
	out := []apiTarget{}
	for _, t := range ts {
		at := apiTarget{
			ID: t.ID, Name: t.Name, Platform: t.Platform, Environment: envName[t.EnvironmentID],
			CollectedBy: t.AgentID, CollectorStatus: t.CollectorStatus, CollectorError: t.CollectorError,
		}
		if at.CollectedBy == "" {
			at.CollectedBy = "server"
		}
		if !t.LastSnapshotAt.IsZero() {
			last := t.LastSnapshotAt
			at.LastSnapshotAt = &last
		}
		out = append(out, at)
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *PublicHandler) events(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	q := r.URL.Query()
	f := store.EventFilter{Limit: 100}
	if n, err := strconv.Atoi(q.Get("limit")); err == nil && n > 0 && n <= 1000 {
		f.Limit = n
	}
	if b := q.Get("before"); b != "" {
		t, err := time.Parse(time.RFC3339, b)
		if err != nil {
			writeProblem(w, http.StatusBadRequest, "Invalid before", "use RFC 3339, e.g. 2026-10-02T08:00:00Z")
			return
		}
		f.Before = t
	}
	if s := q.Get("since"); s != "" {
		t, err := parseSince(s, time.Now())
		if err != nil {
			writeProblem(w, http.StatusBadRequest, "Invalid since", "use a duration like 2h or 30m, or RFC 3339")
			return
		}
		f.Since = t
	}
	if t := q.Get("type"); t != "" {
		f.Types = strings.Split(t, ",")
	}
	if name := q.Get("service"); name != "" {
		svc, err := h.store.GetServiceByName(r.Context(), p.Scope, name)
		if err != nil {
			writeProblem(w, http.StatusNotFound, "Unknown service", name)
			return
		}
		f.ServiceID = svc.ID
	}
	if name := q.Get("environment"); name != "" {
		env, err := h.store.GetEnvironmentByName(r.Context(), p.Scope, name)
		if err != nil {
			writeProblem(w, http.StatusNotFound, "Unknown environment", name)
			return
		}
		f.EnvironmentID = env.ID
	}
	evs, err := h.store.ListEvents(r.Context(), p.Scope, f)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	o, err := versions.LoadOverview(r.Context(), h.store, p.Scope)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	targetName := map[string]string{}
	for _, t := range o.Targets {
		targetName[t.ID] = t.Name
	}
	type apiEvent struct {
		ID          string    `json:"id"`
		Type        string    `json:"type"`
		Service     string    `json:"service,omitempty"`
		App         string    `json:"app,omitempty"`
		Environment string    `json:"environment,omitempty"`
		Target      string    `json:"target,omitempty"`
		From        string    `json:"from,omitempty"`
		To          string    `json:"to,omitempty"`
		Note        string    `json:"note,omitempty"`
		Source      string    `json:"source"`
		At          time.Time `json:"at"`
	}
	out := []apiEvent{}
	for _, e := range evs {
		out = append(out, apiEvent{
			ID: e.ID, Type: e.Type, Service: o.Services[e.ServiceID].Name, App: e.App, Environment: o.Envs[e.EnvironmentID].Name,
			Target: targetName[e.TargetID], From: e.FromVersion, To: e.ToVersion, Note: versions.EventNote(e), Source: e.Source, At: e.At,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

// parseSince reads "2h", "30m", "7d" (a time before now) or an RFC 3339 time.
func parseSince(s string, now time.Time) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	if days, ok := strings.CutSuffix(s, "d"); ok {
		n, err := strconv.Atoi(days)
		if err != nil || n < 0 {
			return time.Time{}, fmt.Errorf("invalid since %q", s)
		}
		return now.Add(-time.Duration(n) * 24 * time.Hour), nil
	}
	d, err := time.ParseDuration(s)
	if err != nil || d < 0 {
		return time.Time{}, fmt.Errorf("invalid since %q", s)
	}
	return now.Add(-d), nil
}

func (h *PublicHandler) promotions(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	o, err := versions.LoadOverview(r.Context(), h.store, p.Scope)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	list, err := versions.Promotions(r.Context(), h.store, p.Scope, o)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if err := versions.AddVulnDeltas(r.Context(), h.store, p.Scope, list); err != nil {
		h.fail(w, r, err)
		return
	}
	if err := versions.AddLicenseDeltas(r.Context(), h.store, p.Scope, list); err != nil {
		h.fail(w, r, err)
		return
	}
	type apiLicense struct {
		Package string `json:"package"`
		Version string `json:"version"`
		License string `json:"license"`
		Class   string `json:"class"`
		Was     string `json:"was,omitempty"`
	}
	type apiRelease struct {
		Version      string     `json:"version"`
		PublishedAt  *time.Time `json:"published_at,omitempty"`
		ReleaseNotes string     `json:"release_notes,omitempty"`
	}
	type apiPromotion struct {
		Service  string        `json:"service"`
		App      string        `json:"app,omitempty"`
		From     string        `json:"from"`
		To       string        `json:"to"`
		Version  string        `json:"version"`
		Running  string        `json:"running"`
		Since    time.Time     `json:"since"`
		Releases []apiRelease  `json:"releases"`
		Vulns    *apiVulnDelta `json:"vulnerabilities,omitempty"`
		Licenses []apiLicense  `json:"licenses,omitempty"`
	}
	out := []apiPromotion{}
	for _, pr := range list {
		ap := apiPromotion{Service: pr.Service.Name, App: pr.App, From: pr.From.Name, To: pr.To.Name, Version: pr.Version, Running: pr.Running, Since: pr.Since, Releases: []apiRelease{}}
		for _, rel := range pr.Releases {
			ar := apiRelease{Version: rel.Version, ReleaseNotes: rel.ChangelogURL}
			if !rel.PublishedAt.IsZero() {
				t := rel.PublishedAt
				ar.PublishedAt = &t
			}
			ap.Releases = append(ap.Releases, ar)
		}
		ap.Vulns = toAPIVulnDelta(pr.Vulns)
		for _, c := range pr.Licenses.Changes {
			ap.Licenses = append(ap.Licenses, apiLicense{Package: c.Name, Version: c.Version, License: c.License, Class: string(c.Class), Was: c.Was})
		}
		out = append(out, ap)
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *PublicHandler) delivery(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	window := 30 * 24 * time.Hour
	if s := r.URL.Query().Get("window"); s != "" {
		since, err := parseSince(s, time.Now())
		if err != nil || !since.Before(time.Now()) {
			writeProblem(w, http.StatusBadRequest, "Invalid window", "use a duration like 7d or 720h")
			return
		}
		window = time.Since(since).Round(time.Second)
	}
	o, err := versions.LoadOverview(r.Context(), h.store, p.Scope)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	stats, err := versions.DeliveryStats(r.Context(), h.store, p.Scope, o, window, time.Now())
	if err != nil {
		h.fail(w, r, err)
		return
	}
	type apiEnv struct {
		Environment string     `json:"environment"`
		Deploys     int        `json:"deploys"`
		Rollbacks   int        `json:"rollbacks"`
		LastDeploy  *time.Time `json:"last_deploy,omitempty"`
	}
	type apiLead struct {
		From          string  `json:"from"`
		To            string  `json:"to"`
		MedianSeconds float64 `json:"median_seconds"`
		Samples       int     `json:"samples"`
	}
	type apiDelivery struct {
		Service      string    `json:"service"`
		Environments []apiEnv  `json:"environments"`
		LeadTimes    []apiLead `json:"lead_times"`
	}
	out := []apiDelivery{}
	for _, d := range stats {
		ad := apiDelivery{Service: d.Service.Name, Environments: []apiEnv{}, LeadTimes: []apiLead{}}
		for _, e := range d.Envs {
			ae := apiEnv{Environment: e.Env.Name, Deploys: e.Deploys, Rollbacks: e.Rollbacks}
			if !e.LastDeploy.IsZero() {
				t := e.LastDeploy
				ae.LastDeploy = &t
			}
			ad.Environments = append(ad.Environments, ae)
		}
		for _, lt := range d.LeadTimes {
			ad.LeadTimes = append(ad.LeadTimes, apiLead{From: lt.From.Name, To: lt.To.Name, MedianSeconds: lt.Median.Seconds(), Samples: lt.Samples})
		}
		out = append(out, ad)
	}
	writeJSON(w, http.StatusOK, out)
}

// atParam reads ?at= (RFC 3339, or 2006-01-02T15:04 in UTC); nil means now.
func atParam(w http.ResponseWriter, r *http.Request) (*time.Time, bool) {
	s := r.URL.Query().Get("at")
	if s == "" {
		return nil, true
	}
	t, err := ParseAt(s)
	if err != nil {
		writeProblem(w, http.StatusBadRequest, "Invalid at", "use RFC 3339, e.g. 2026-09-12T14:00:00Z")
		return nil, false
	}
	return &t, true
}

// ParseAt reads a point in time: RFC 3339, or "2006-01-02T15:04" / "2006-01-02 15:04" in UTC.
func ParseAt(s string) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	for _, layout := range []string{"2006-01-02T15:04", "2006-01-02 15:04", "2006-01-02"} {
		if t, err := time.ParseInLocation(layout, s, time.UTC); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("invalid time %q", s)
}

func (h *PublicHandler) inventory(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	at, ok := atParam(w, r)
	if !ok {
		return
	}
	items, err := versions.Inventory(r.Context(), h.store, p.Scope, at)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if name := r.URL.Query().Get("environment"); name != "" {
		if _, err := h.store.GetEnvironmentByName(r.Context(), p.Scope, name); err != nil {
			writeProblem(w, http.StatusNotFound, "Unknown environment", name)
			return
		}
		kept := items[:0]
		for _, i := range items {
			if i.Environment == name {
				kept = append(kept, i)
			}
		}
		items = kept
	}
	stamp := ""
	if at != nil {
		stamp = "-" + at.UTC().Format("20060102-1504")
	}
	switch r.URL.Query().Get("format") {
	case "csv":
	case "cyclonedx":
		sboms, err := h.store.ListImageSBOMs(r.Context(), p.Scope)
		if err != nil {
			h.fail(w, r, err)
			return
		}
		workspace := "goliash"
		if all, err := h.store.ListWorkspaces(r.Context()); err == nil {
			for _, ws := range all {
				if ws.ID == p.Scope.WorkspaceID {
					workspace = ws.Name
				}
			}
		}
		w.Header().Set("Content-Disposition", `attachment; filename="goliash-inventory`+stamp+`.cdx.json"`)
		w.Header().Set("Content-Type", "application/vnd.cyclonedx+json; version=1.6")
		_ = json.NewEncoder(w).Encode(versions.InventoryBOM(items, sboms, workspace, buildinfo.Version, at, time.Now()))
		return
	default:
		writeJSON(w, http.StatusOK, items)
		return
	}
	name := "goliash-inventory" + stamp + ".csv"
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	cw := csv.NewWriter(w)
	_ = cw.Write([]string{"environment", "target", "platform", "service", "namespace", "workload", "kind", "container", "image", "tag", "digest", "running", "first_seen", "size_bytes", "base", "base_status"})
	for _, i := range items {
		_ = cw.Write([]string{
			i.Environment, i.Target, i.Platform, i.Service, i.Namespace, i.Workload, i.Kind, i.Container,
			i.Image, i.Tag, i.Digest, strconv.Itoa(i.Running), i.FirstSeen.UTC().Format(time.RFC3339),
			strconv.FormatInt(i.SizeBytes, 10), i.Base, i.BaseStatus,
		})
	}
	cw.Flush()
}

// notificationsSent is what Goliash sent, newest first: each message once however many channels it went to,
// as the bell lists it (the last 7 days, or since ?since=).
func (h *PublicHandler) notificationsSent(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	since := time.Now().Add(-7 * 24 * time.Hour)
	if s := r.URL.Query().Get("since"); s != "" {
		t, err := parseSince(s, time.Now())
		if err != nil {
			writeProblem(w, http.StatusBadRequest, "Invalid since", "use a duration like 2h or 7d, or RFC 3339")
			return
		}
		since = t
	}
	feed, err := h.store.NotificationFeed(r.Context(), p.Scope, since, 100)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	name := h.workspaceName(r.Context(), p)
	type sent struct {
		SentAt time.Time       `json:"sent_at"`
		Title  string          `json:"title"`
		Mode   string          `json:"mode"`
		Items  []notifier.Item `json:"items"`
	}
	out := []sent{}
	for _, m := range feed {
		msg := notifier.Message{Workspace: name, Digest: m.Mode != "instant" || len(m.Items) > 1}
		for _, raw := range m.Items {
			var it notifier.Item
			if json.Unmarshal(raw, &it) == nil {
				it.Apps = nil // only for rule filters
				msg.Items = append(msg.Items, it)
			}
		}
		if len(msg.Items) > 0 {
			out = append(out, sent{SentAt: m.SentAt, Title: msg.Title(), Mode: m.Mode, Items: msg.Items})
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// workspaceName names the request's workspace; an API token's principal carries only its ID.
func (h *PublicHandler) workspaceName(ctx context.Context, p auth.Principal) string {
	if p.Workspace.Name != "" {
		return p.Workspace.Name
	}
	wss, err := h.store.ListWorkspaces(ctx)
	if err != nil {
		return ""
	}
	for _, ws := range wss {
		if ws.ID == p.Scope.WorkspaceID {
			return ws.Name
		}
	}
	return ""
}

// summary is Goliash in a dozen numbers, for widgets on a phone, a desktop or a wall screen.
func (h *PublicHandler) summary(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	ctx := r.Context()
	now := time.Now()
	o, err := versions.LoadOverview(ctx, h.store, p.Scope)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	pos, err := versions.LoadPosture(ctx, h.store, p.Scope, o, now)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	acks, err := h.store.ListAcks(ctx, p.Scope)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	drifting := map[string]bool{}
	for key, ds := range o.Drifts {
		if len(ds) > 0 {
			svc, _, _ := strings.Cut(key, "|")
			drifting[svc] = true
		}
	}
	updates, exploited := 0, 0
	for _, u := range versions.LoadUpdates(ctx, h.store, p.Scope, o, acks, now) {
		if u.Acked {
			continue
		}
		updates++
		if len(u.Exploited) > 0 {
			exploited++
		}
	}
	writeJSON(w, http.StatusOK, struct {
		Workspace  string    `json:"workspace"`
		Production string    `json:"production"`
		Services   int       `json:"services"`
		Drifting   int       `json:"drifting"`
		Updates    int       `json:"updates"`
		Exploited  int       `json:"exploited"`
		Behind     int       `json:"behind"`
		Median     int       `json:"median_exposure_days"`
		Behind90   int       `json:"behind_90_days"`
		EOLPassed  int       `json:"end_of_life_passed"`
		Stale      int       `json:"stale_targets"`
		At         time.Time `json:"generated_at"`
		AtText     string    `json:"generated"` // for widgets that cannot format a time
	}{
		Workspace: h.workspaceName(ctx, p), Production: pos.Prod.Name, Services: len(o.Services), Drifting: len(drifting),
		Updates: updates, Exploited: exploited, Behind: pos.ProdExposed(), Median: pos.ProdMedian,
		Behind90: pos.Prod90, EOLPassed: pos.EOLPassed, Stale: len(o.Stale), At: now.UTC(),
		AtText: now.UTC().Format("Jan 2, 15:04 UTC"),
	})
}

// apiReading is production's posture on one day of its trend.
type apiReading struct {
	Day       string `json:"day"`
	Behind    int    `json:"behind"`
	Median    int    `json:"median_exposure_days"`
	P90       int    `json:"p90_exposure_days"`
	Behind30  int    `json:"behind_30_days"`
	Behind90  int    `json:"behind_90_days"`
	EOLPassed int    `json:"end_of_life_passed"`
	Accepted  int    `json:"accepted_risk"`
}

// weekly keeps one reading a week, counted back from the newest, oldest first: the trend without the daily noise.
func weekly(hist []store.PostureSample) []store.PostureSample {
	var out []store.PostureSample
	next := ""
	for i := len(hist) - 1; i >= 0; i-- {
		if next != "" && hist[i].Day > next {
			continue
		}
		out = append(out, hist[i])
		day, err := time.Parse(time.DateOnly, hist[i].Day)
		if err != nil {
			break
		}
		next = day.AddDate(0, 0, -7).Format(time.DateOnly)
	}
	slices.Reverse(out)
	return out
}

// posture is the security posture: how long production has run behind available releases, end of life, accepted
// risk and blind spots, with every exposure (production only, or every environment with ?all=true).
func (h *PublicHandler) posture(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	ctx := r.Context()
	o, err := versions.LoadOverview(ctx, h.store, p.Scope)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	pos, err := versions.LoadPosture(ctx, h.store, p.Scope, o, time.Now())
	if err != nil {
		h.fail(w, r, err)
		return
	}
	type apiExposure struct {
		Service      string    `json:"service"`
		App          string    `json:"app,omitempty"`
		Environment  string    `json:"environment"`
		Running      string    `json:"running"`
		Fix          string    `json:"fix,omitempty"`
		ReleaseNotes string    `json:"release_notes,omitempty"`
		Days         int       `json:"exposed_days"`
		Since        time.Time `json:"since"`
		SinceKnown   bool      `json:"since_known"` // false: when Goliash first saw the release, a lower bound
		Why          string    `json:"why"`
		EOL          string    `json:"end_of_life,omitempty"`
		Accepted     bool      `json:"accepted"`
	}
	out := struct {
		Production string        `json:"production"`
		Running    int           `json:"services_running"`
		Median     int           `json:"median_exposure_days"`
		P90        int           `json:"p90_exposure_days"`
		Behind30   int           `json:"behind_30_days"`
		Behind90   int           `json:"behind_90_days"`
		EOLPassed  int           `json:"end_of_life_passed"`
		EOLSoon    int           `json:"end_of_life_soon"`
		Accepted   int           `json:"accepted_risk"`
		Stale      []string      `json:"stale_targets"`
		NoUpstream []string      `json:"no_known_upstream"`
		OldBase    []string      `json:"outdated_base"`
		Exposures  []apiExposure `json:"exposures"`
		History    []apiReading  `json:"history"`
	}{
		Production: pos.Prod.Name, Running: pos.ProdRunning, Median: pos.ProdMedian, P90: pos.ProdP90,
		Behind30: pos.Prod30, Behind90: pos.Prod90, EOLPassed: pos.EOLPassed, EOLSoon: pos.EOLSoon, Accepted: pos.Accepted,
		Stale: append([]string{}, pos.Stale...), NoUpstream: append([]string{}, pos.NoUpstream...), Exposures: []apiExposure{},
		OldBase: append([]string{}, pos.OutdatedBase...),
		History: []apiReading{},
	}
	hist, err := h.store.PostureHistory(ctx, p.Scope, time.Now().AddDate(0, 0, -91).UTC().Format(time.DateOnly))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	for _, s := range weekly(hist) {
		out.History = append(out.History, apiReading{
			Day: s.Day, Behind: s.Exposed, Median: s.Median, P90: s.P90,
			Behind30: s.Behind30, Behind90: s.Behind90, EOLPassed: s.EOLPassed, Accepted: s.Accepted,
		})
	}
	all := r.URL.Query().Get("all") == "true"
	for _, ex := range pos.Exposures {
		if !all && ex.Environment.ID != pos.Prod.ID {
			continue
		}
		out.Exposures = append(out.Exposures, apiExposure{
			Service: ex.Service.Name, App: ex.App, Environment: ex.Environment.Name, Running: ex.Running, Fix: ex.Target,
			ReleaseNotes: ex.TargetURL, Days: ex.Days, Since: ex.BehindSince, SinceKnown: ex.Known,
			Why: versions.UrgencyLabels[ex.Urgency], EOL: ex.EOL, Accepted: ex.Acked,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *PublicHandler) updates(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	ctx := r.Context()
	o, err := versions.LoadOverview(ctx, h.store, p.Scope)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	acks, err := h.store.ListAcks(ctx, p.Scope)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	type apiUpdate struct {
		Service      string        `json:"service"`
		Owner        string        `json:"owner,omitempty"`
		App          string        `json:"app,omitempty"`
		Environment  string        `json:"environment"`
		Running      string        `json:"running"`
		Target       string        `json:"target,omitempty"`
		ReleaseNotes string        `json:"release_notes,omitempty"`
		Jump         string        `json:"jump,omitempty"`
		Behind       string        `json:"behind,omitempty"`
		EOL          string        `json:"eol,omitempty"`
		Urgency      string        `json:"urgency"`
		Since        time.Time     `json:"since"`
		Acknowledged bool          `json:"acknowledged"`
		Exploited    []string      `json:"exploited,omitempty"`
		Critical     int           `json:"critical_vulnerabilities,omitempty"`
		Vulns        *apiVulnDelta `json:"vulnerabilities,omitempty"`
	}
	out := []apiUpdate{}
	for _, u := range versions.LoadUpdates(ctx, h.store, p.Scope, o, acks, time.Now()) {
		out = append(out, apiUpdate{
			Service: u.Service.Name, Owner: u.Service.Owner, App: u.App, Environment: u.Environment.Name,
			Running: u.Running, Target: u.Target, ReleaseNotes: u.TargetURL, Jump: string(u.Jump), Behind: u.Behind,
			EOL: u.EOL, Urgency: versions.UrgencyLabels[u.Urgency], Since: u.Since, Acknowledged: u.Acked,
			Exploited: u.Exploited, Critical: u.Critical, Vulns: toAPIVulnDelta(u.Vulns),
		})
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *PublicHandler) hygiene(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	findings, err := versions.LoadHygiene(r.Context(), h.store, p.Scope)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	type apiFinding struct {
		Kind     string   `json:"kind"`
		Severity string   `json:"severity"`
		Image    string   `json:"image"`
		Detail   string   `json:"detail"`
		Where    []string `json:"where"`
	}
	out := []apiFinding{}
	for _, f := range findings {
		out = append(out, apiFinding{Kind: f.Kind, Severity: f.Severity, Image: f.Image, Detail: f.Detail, Where: f.Where})
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *PublicHandler) drifts(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	o, err := versions.LoadOverview(r.Context(), h.store, p.Scope)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	out := []apiDrift{}
	for _, ds := range o.Drifts {
		for _, d := range ds {
			out = append(out, toAPIDrift(o, d))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Since.Before(out[j].Since) })
	writeJSON(w, http.StatusOK, out)
}

func (h *PublicHandler) createAck(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	var in struct {
		Service      string     `json:"service"`
		Environment  string     `json:"environment"`
		Kind         string     `json:"kind"`
		UntilVersion string     `json:"until_version"`
		Until        *time.Time `json:"until"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&in); err != nil {
		writeProblem(w, http.StatusBadRequest, "Invalid JSON", err.Error())
		return
	}
	if in.Kind != "release" && in.Kind != "drift" {
		writeProblem(w, http.StatusBadRequest, "Invalid kind", "kind must be release or drift")
		return
	}
	if in.UntilVersion == "" && in.Until == nil {
		writeProblem(w, http.StatusBadRequest, "Missing limit", "set until_version or until")
		return
	}
	svc, err := h.store.GetServiceByName(r.Context(), p.Scope, in.Service)
	if err != nil {
		writeProblem(w, http.StatusNotFound, "Unknown service", in.Service)
		return
	}
	a := store.Ack{Scope: p.Scope, ServiceID: svc.ID, Kind: in.Kind, UntilVersion: in.UntilVersion, CreatedBy: p.Name()}
	if in.Until != nil {
		a.UntilAt = *in.Until
	}
	if in.Environment != "" {
		env, err := h.store.GetEnvironmentByName(r.Context(), p.Scope, in.Environment)
		if err != nil {
			writeProblem(w, http.StatusNotFound, "Unknown environment", in.Environment)
			return
		}
		a.EnvironmentID = env.ID
	}
	if a, err = h.store.CreateAck(r.Context(), a); err != nil {
		h.fail(w, r, err)
		return
	}
	_ = h.store.Audit(r.Context(), store.AuditEntry{
		OrgID: p.Scope.OrgID, WorkspaceID: p.Scope.WorkspaceID, Actor: p.Name(), Action: "ack.create",
		Details: map[string]string{"service": svc.Name, "kind": a.Kind, "until_version": a.UntilVersion},
	})
	writeJSON(w, http.StatusCreated, map[string]string{"id": a.ID})
}

// metrics writes Prometheus text exposition: what runs where, whether it is
// behind upstream, and how long drift has been open.
func (h *PublicHandler) metrics(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	o, err := versions.LoadOverview(r.Context(), h.store, p.Scope)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	var b strings.Builder
	b.WriteString("# HELP goliash_deployed_version_info Version running per service and environment (value: running replicas).\n")
	b.WriteString("# TYPE goliash_deployed_version_info gauge\n")
	for _, row := range o.Matrix.Rows {
		for ei, c := range row.Cells {
			for _, v := range c.Versions {
				fmt.Fprintf(&b, "goliash_deployed_version_info{service=%q,environment=%q,version=%q} %d\n",
					row.Service.Name, o.Matrix.Environments[ei].Name, v.Tag, v.Running)
			}
		}
	}
	var outdated, days []string
	for _, row := range o.Matrix.Rows {
		for ei, c := range row.Cells {
			if c.Empty() {
				continue
			}
			env := o.Matrix.Environments[ei]
			isOutdated := 0
			for _, d := range o.DriftsAt(row.Service.ID, env.ID) {
				if d.Kind == "upstream" {
					isOutdated = 1
				}
				app := "" // only for services compared per application, so other series stay as they were
				if d.App != "" {
					app = fmt.Sprintf(",app=%q", d.App)
				}
				days = append(days, fmt.Sprintf("goliash_drift_days{service=%q,environment=%q%s,kind=%q} %.2f",
					row.Service.Name, env.Name, app, d.Kind, time.Since(d.Since).Hours()/24))
			}
			outdated = append(outdated, fmt.Sprintf("goliash_outdated{service=%q,environment=%q} %d", row.Service.Name, env.Name, isOutdated))
		}
	}
	b.WriteString("# HELP goliash_outdated 1 when the environment runs a version behind upstream by the tracked jump.\n")
	b.WriteString("# TYPE goliash_outdated gauge\n")
	for _, l := range outdated {
		b.WriteString(l + "\n")
	}
	if stats, err := versions.DeliveryStats(r.Context(), h.store, p.Scope, o, 30*24*time.Hour, time.Now()); err == nil {
		b.WriteString("# HELP goliash_deploys Versions that arrived in an environment in the last 30 days.\n")
		b.WriteString("# TYPE goliash_deploys gauge\n")
		for _, d := range stats {
			for _, e := range d.Envs {
				fmt.Fprintf(&b, "goliash_deploys{service=%q,environment=%q} %d\n", d.Service.Name, e.Env.Name, e.Deploys)
			}
		}
		b.WriteString("# HELP goliash_rollbacks Changes back to an older version in an environment in the last 30 days.\n")
		b.WriteString("# TYPE goliash_rollbacks gauge\n")
		for _, d := range stats {
			for _, e := range d.Envs {
				fmt.Fprintf(&b, "goliash_rollbacks{service=%q,environment=%q} %d\n", d.Service.Name, e.Env.Name, e.Rollbacks)
			}
		}
		b.WriteString("# HELP goliash_lead_time_seconds Median time a version took from one environment to the next, last 30 days.\n")
		b.WriteString("# TYPE goliash_lead_time_seconds gauge\n")
		for _, d := range stats {
			for _, lt := range d.LeadTimes {
				if lt.Samples > 0 {
					fmt.Fprintf(&b, "goliash_lead_time_seconds{service=%q,from=%q,to=%q} %.0f\n", d.Service.Name, lt.From.Name, lt.To.Name, lt.Median.Seconds())
				}
			}
		}
	}
	if findings, err := versions.LoadHygiene(r.Context(), h.store, p.Scope); err == nil {
		byKind := map[string]int{"moving-tag": 0, "retagged": 0, "untrusted-registry": 0, "outdated-base": 0, "over-budget": 0, "unexpected-signer": 0, "unpinned": 0}
		for _, f := range findings {
			byKind[f.Kind]++
		}
		b.WriteString("# HELP goliash_image_hygiene_findings Images with a moving tag, a tag pushed again, an untrusted registry, an outdated base, a size over budget, an unexpected signer or no digest.\n")
		b.WriteString("# TYPE goliash_image_hygiene_findings gauge\n")
		for _, k := range []string{"moving-tag", "retagged", "untrusted-registry", "outdated-base", "over-budget", "unexpected-signer", "unpinned"} {
			fmt.Fprintf(&b, "goliash_image_hygiene_findings{kind=%q} %d\n", k, byKind[k])
		}
	}
	if lic, err := versions.LoadLicenseReport(r.Context(), h.store, p.Scope, ""); err == nil {
		b.WriteString("# HELP goliash_license_packages Packages in running images per license class (application packages, and operating system ones apart).\n")
		b.WriteString("# TYPE goliash_license_packages gauge\n")
		for _, c := range versions.LicenseClasses {
			fmt.Fprintf(&b, "goliash_license_packages{class=%q,kind=\"app\"} %d\n", c, lic.Counts[c])
			fmt.Fprintf(&b, "goliash_license_packages{class=%q,kind=\"os\"} %d\n", c, lic.OSCounts[c])
		}
	}
	h.vulnMetrics(r.Context(), &b, p.Scope, o)
	if issues, err := versions.RolloutIssues(r.Context(), h.store, p.Scope, o, time.Now()); err == nil {
		rolloutMetrics(&b, issues)
	}
	h.imageSizeMetrics(r.Context(), &b, p.Scope, o)
	h.gitOpsMetrics(r.Context(), &b, p.Scope, o, time.Now())
	h.serverMetrics(r.Context(), &b, p.Scope)
	if pos, err := versions.LoadPosture(r.Context(), h.store, p.Scope, o, time.Now()); err == nil {
		postureMetrics(&b, pos)
	}
	b.WriteString("# HELP goliash_drift_days How long a drift has been open, in days.\n")
	b.WriteString("# TYPE goliash_drift_days gauge\n")
	for _, l := range days {
		b.WriteString(l + "\n")
	}
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	_, _ = w.Write([]byte(b.String()))
}

// imageSizeMetrics describes each service's main images per environment: what pulling them downloads (the
// largest when an environment runs several), the size budget, how long a base has had a newer build, and which
// supply-chain evidence every image has.
func (h *PublicHandler) imageSizeMetrics(ctx context.Context, b *strings.Builder, sc store.Scope, o versions.Overview) {
	active, err := h.store.ListActiveInstances(ctx, sc)
	if err != nil {
		return
	}
	known, err := h.store.ListImageEvidence(ctx, sc)
	if err != nil {
		return
	}
	type key struct{ service, env string }
	type images struct {
		size, layers             int64
		baseDays                 float64
		based                    bool // some image's base was compared
		checked                  bool // some image's evidence was looked up
		signed, sbom, provenance bool // every looked-up image has it
	}
	seen := map[key]*images{}
	for _, in := range active {
		svc, ok := o.Services[in.ServiceID]
		if !in.IsMain || !ok || in.Digest == "" {
			continue
		}
		k := key{svc.Name, o.Envs[in.EnvironmentID].Name}
		m := seen[k]
		if m == nil {
			m = &images{signed: true, sbom: true, provenance: true}
			seen[k] = m
		}
		ev, ok := known[versions.ParseImage(in.Image).Repo()+"@"+in.Digest]
		if !ok {
			continue
		}
		if ev.Size > m.size {
			m.size, m.layers = ev.Size, int64(len(ev.Layers))
		}
		switch ev.BaseStatus {
		case "current":
			m.based = true
		case "outdated":
			m.based = true
			if !ev.BaseSince.IsZero() {
				m.baseDays = max(m.baseDays, time.Since(ev.BaseSince).Hours()/24)
			}
		}
		if ev.Error == "" && !ev.CheckedAt.IsZero() {
			m.checked = true
			m.signed, m.sbom, m.provenance = m.signed && ev.Signed, m.sbom && ev.SBOM, m.provenance && ev.Provenance
		}
	}
	keys := make([]key, 0, len(seen))
	for k := range seen {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].service != keys[j].service {
			return keys[i].service < keys[j].service
		}
		return keys[i].env < keys[j].env
	})
	b.WriteString("# HELP goliash_image_size_bytes What pulling the service's image downloads (compressed layers and config).\n")
	b.WriteString("# TYPE goliash_image_size_bytes gauge\n")
	for _, k := range keys {
		if m := seen[k]; m.size > 0 {
			fmt.Fprintf(b, "goliash_image_size_bytes{service=%q,environment=%q} %d\n", k.service, k.env, m.size)
		}
	}
	b.WriteString("# HELP goliash_image_layers Layers in the service's image (the one goliash_image_size_bytes measures).\n")
	b.WriteString("# TYPE goliash_image_layers gauge\n")
	for _, k := range keys {
		if m := seen[k]; m.layers > 0 {
			fmt.Fprintf(b, "goliash_image_layers{service=%q,environment=%q} %d\n", k.service, k.env, m.layers)
		}
	}
	b.WriteString("# HELP goliash_image_size_budget_bytes The largest image a service's policy allows (max_size_mb).\n")
	b.WriteString("# TYPE goliash_image_size_budget_bytes gauge\n")
	budgets := make([]store.Service, 0, len(o.Services))
	for _, svc := range o.Services {
		if versions.SizeBudget(svc) > 0 {
			budgets = append(budgets, svc)
		}
	}
	sort.Slice(budgets, func(i, j int) bool { return budgets[i].Name < budgets[j].Name })
	for _, svc := range budgets {
		fmt.Fprintf(b, "goliash_image_size_budget_bytes{service=%q} %d\n", svc.Name, versions.SizeBudget(svc))
	}
	b.WriteString("# HELP goliash_image_base_behind_days Days the image's base tag has had a newer build than the one the image is built on (0: current).\n")
	b.WriteString("# TYPE goliash_image_base_behind_days gauge\n")
	for _, k := range keys {
		if m := seen[k]; m.based {
			fmt.Fprintf(b, "goliash_image_base_behind_days{service=%q,environment=%q} %.2f\n", k.service, k.env, m.baseDays)
		}
	}
	b.WriteString("# HELP goliash_image_evidence 1 when every image the service runs in the environment has the evidence: a signature, an SBOM or provenance.\n")
	b.WriteString("# TYPE goliash_image_evidence gauge\n")
	for _, k := range keys {
		m := seen[k]
		if !m.checked {
			continue
		}
		for _, e := range []struct {
			kind string
			has  bool
		}{{"signature", m.signed}, {"sbom", m.sbom}, {"provenance", m.provenance}} {
			fmt.Fprintf(b, "goliash_image_evidence{service=%q,environment=%q,kind=%q} %d\n", k.service, k.env, e.kind, b2i(e.has))
		}
	}
}

func b2i(v bool) int {
	if v {
		return 1
	}
	return 0
}

// postureMetrics is the security posture as metrics: what a CISO's dashboard trends and alerts on.
func postureMetrics(b *strings.Builder, pos versions.Posture) {
	prod := pos.Prod.Name
	b.WriteString("# HELP goliash_exposure_median_days Median days production has run behind an available release (known release dates).\n# TYPE goliash_exposure_median_days gauge\n")
	fmt.Fprintf(b, "goliash_exposure_median_days{environment=%q} %d\n", prod, pos.ProdMedian)
	b.WriteString("# HELP goliash_exposure_p90_days 90th percentile of days production has run behind an available release.\n# TYPE goliash_exposure_p90_days gauge\n")
	fmt.Fprintf(b, "goliash_exposure_p90_days{environment=%q} %d\n", prod, pos.ProdP90)
	b.WriteString("# HELP goliash_exposures Production services behind an available release for at least the given days.\n# TYPE goliash_exposures gauge\n")
	fmt.Fprintf(b, "goliash_exposures{environment=%q,over_days=\"30\"} %d\n", prod, pos.Prod30)
	fmt.Fprintf(b, "goliash_exposures{environment=%q,over_days=\"90\"} %d\n", prod, pos.Prod90)
	b.WriteString("# HELP goliash_end_of_life Production services on a release cycle past or near its end of life.\n# TYPE goliash_end_of_life gauge\n")
	fmt.Fprintf(b, "goliash_end_of_life{environment=%q,state=\"passed\"} %d\n", prod, pos.EOLPassed)
	fmt.Fprintf(b, "goliash_end_of_life{environment=%q,state=\"soon\"} %d\n", prod, pos.EOLSoon)
	b.WriteString("# HELP goliash_accepted_risk Production exposures quieted by an acknowledgement.\n# TYPE goliash_accepted_risk gauge\n")
	fmt.Fprintf(b, "goliash_accepted_risk{environment=%q} %d\n", prod, pos.Accepted)
	b.WriteString("# HELP goliash_exposure_days Days a service has run behind an available release.\n# TYPE goliash_exposure_days gauge\n")
	for _, ex := range pos.Exposures {
		app := ""
		if ex.App != "" {
			app = fmt.Sprintf(",app=%q", ex.App)
		}
		fmt.Fprintf(b, "goliash_exposure_days{service=%q,environment=%q%s} %d\n", ex.Service.Name, ex.Environment.Name, app, ex.Days)
	}
}

// serverMetrics describes this server and the workspace's work in flight: what to
// alert on when Goliash itself is unwell.
func (h *PublicHandler) serverMetrics(ctx context.Context, b *strings.Builder, sc store.Scope) {
	b.WriteString("# HELP goliash_build_info The running server's version.\n# TYPE goliash_build_info gauge\n")
	fmt.Fprintf(b, "goliash_build_info{version=%q} 1\n", buildinfo.Version)
	if h.isLeader != nil {
		leader := 0
		if h.isLeader() {
			leader = 1
		}
		b.WriteString("# HELP goliash_leader 1 when this server runs the background work (one server per database).\n# TYPE goliash_leader gauge\n")
		fmt.Fprintf(b, "goliash_leader %d\n", leader)
	}
	if q, err := h.store.Queues(ctx, sc); err == nil {
		b.WriteString("# HELP goliash_snapshots_pending Snapshots received and not processed yet.\n# TYPE goliash_snapshots_pending gauge\n")
		fmt.Fprintf(b, "goliash_snapshots_pending %d\n", q.PendingSnapshots)
		b.WriteString("# HELP goliash_notifications_queued Notifications not sent yet (due, or waiting for a digest).\n# TYPE goliash_notifications_queued gauge\n")
		fmt.Fprintf(b, "goliash_notifications_queued %d\n", q.QueuedNotifications)
		b.WriteString("# HELP goliash_notifications_failing Notifications not sent that failed at least once.\n# TYPE goliash_notifications_failing gauge\n")
		fmt.Fprintf(b, "goliash_notifications_failing %d\n", q.FailingNotifications)
	}
	if agents, err := h.store.ListAgents(ctx, sc); err == nil {
		counts := map[string]int{"online": 0, "stale": 0, "never": 0, "revoked": 0}
		for _, a := range agents {
			switch {
			case a.ActiveTokens == 0:
				counts["revoked"]++
			case a.LastSeenAt.IsZero():
				counts["never"]++
			case !a.StaleSince.IsZero():
				counts["stale"]++
			default:
				counts["online"]++
			}
		}
		b.WriteString("# HELP goliash_agents Agents by status.\n# TYPE goliash_agents gauge\n")
		for _, st := range []string{"online", "stale", "never", "revoked"} {
			fmt.Fprintf(b, "goliash_agents{status=%q} %d\n", st, counts[st])
		}
	}
}

// vulnerabilities answers "is this vulnerability running?" from the running images' SBOMs and OSV.
func (h *PublicHandler) vulnerabilities(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	q := r.URL.Query()
	envID := ""
	if name := q.Get("environment"); name != "" {
		env, err := h.store.GetEnvironmentByName(r.Context(), p.Scope, name)
		if err != nil {
			writeProblem(w, http.StatusNotFound, "Unknown environment", name)
			return
		}
		envID = env.ID
	}
	rep, err := versions.LoadVulnReport(r.Context(), h.store, p.Scope, envID)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	type apiExploited struct {
		DueDate    string `json:"due_date,omitempty"`
		DateAdded  string `json:"date_added,omitempty"`
		Ransomware bool   `json:"ransomware"`
		PastDue    bool   `json:"past_due"`
	}
	type apiAffected struct {
		Service     string     `json:"service"`
		Environment string     `json:"environment"`
		Image       string     `json:"image"`
		Package     string     `json:"package"`
		Fix         string     `json:"fix,omitempty"`
		Since       *time.Time `json:"since,omitempty"`
	}
	type apiFinding struct {
		ID         string        `json:"id"`
		Advisories []string      `json:"advisories"`
		Aliases    []string      `json:"aliases"`
		Summary    string        `json:"summary,omitempty"`
		Severity   string        `json:"severity,omitempty"`
		Exploited  *apiExploited `json:"exploited,omitempty"`
		Fixable    bool          `json:"fixable"`
		Since      *time.Time    `json:"running_since,omitempty"`
		Affected   []apiAffected `json:"affected"`
	}
	query := strings.ToUpper(strings.TrimSpace(q.Get("q")))
	onlyExploited := q.Get("exploited") == "true"
	out := struct {
		Images   int          `json:"images"`
		WithSBOM int          `json:"images_with_sbom"`
		Packages int          `json:"packages"`
		Complete bool         `json:"complete"`
		Findings []apiFinding `json:"findings"`
	}{Images: rep.Images, WithSBOM: rep.WithSBOM, Packages: rep.Packages, Complete: rep.Pending == 0 && rep.Unasked == 0, Findings: []apiFinding{}}
	if q.Get("format") == "csv" {
		var shown []versions.VulnFinding
		for _, f := range rep.Findings {
			if (!onlyExploited || f.Exploited != nil) && (query == "" || versions.FindingMatches(f, query)) {
				shown = append(shown, f)
			}
		}
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		_ = versions.WriteVulnsCSV(w, shown, time.Now())
		return
	}
	for _, f := range rep.Findings {
		if onlyExploited && f.Exploited == nil {
			continue
		}
		if query != "" && !versions.FindingMatches(f, query) {
			continue
		}
		af := apiFinding{
			ID: f.Key, Advisories: f.IDs, Aliases: f.Aliases, Summary: f.Summary, Severity: strings.ToLower(f.Severity),
			Fixable: f.Fixable, Affected: []apiAffected{},
		}
		if af.Aliases == nil {
			af.Aliases = []string{}
		}
		if f.Exploited != nil {
			af.Exploited = &apiExploited{DueDate: f.Exploited.DueDate, DateAdded: f.Exploited.DateAdded, Ransomware: f.Exploited.Ransomware, PastDue: f.PastDue(time.Now())}
		}
		if !f.Since.IsZero() {
			t := f.Since
			af.Since = &t
		}
		for _, a := range f.Affected {
			aa := apiAffected{Service: a.Service, Environment: a.Env, Image: a.Image, Package: a.Package, Fix: a.Fix}
			if !a.Since.IsZero() {
				t := a.Since
				aa.Since = &t
			}
			af.Affected = append(af.Affected, aa)
		}
		out.Findings = append(out.Findings, af)
	}
	writeJSON(w, http.StatusOK, out)
}

// status is Goliash's own health for the workspace, as the Status page shows it: one check per part with
// its state (ok, warn, bad, off) and the worst of them.
func (h *PublicHandler) status(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	hl, err := versions.LoadHealth(r.Context(), h.store, h.checker, p.Scope, h.version, h.updateCheck)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	type apiFreeze struct {
		Until time.Time `json:"until"`
		Note  string    `json:"note,omitempty"`
	}
	out := struct {
		versions.Health
		ChangeFreeze *apiFreeze `json:"change_freeze,omitempty"`
	}{Health: hl}
	if f, ok, err := h.store.ActiveFreeze(r.Context(), p.Scope, time.Now()); err == nil && ok {
		out.ChangeFreeze = &apiFreeze{Until: f.EndsAt, Note: f.Note}
	}
	writeJSON(w, http.StatusOK, out)
}

// vulnMetrics are the known vulnerabilities of running images per environment: how many per severity and
// whether exploited, those past CISA's due date, and how long the oldest exploited one has run.
func (h *PublicHandler) vulnMetrics(ctx context.Context, b *strings.Builder, sc store.Scope, o versions.Overview) {
	type count struct {
		env, severity string
		exploited     bool
	}
	counts := map[count]int{}
	pastDue, oldest := map[string]int{}, map[string]float64{}
	now := time.Now()
	var envs []string
	for _, e := range o.Matrix.Environments {
		rep, err := versions.LoadVulnReport(ctx, h.store, sc, e.ID)
		if err != nil {
			return
		}
		if rep.WithSBOM == 0 {
			continue
		}
		envs = append(envs, e.Name)
		for _, f := range rep.Findings {
			sev := strings.ToLower(f.Severity)
			if sev == "" {
				sev = "unknown"
			}
			counts[count{e.Name, sev, f.Exploited != nil}]++
			if f.PastDue(now) {
				pastDue[e.Name]++
			}
			if f.Exploited != nil && !f.Since.IsZero() {
				oldest[e.Name] = max(oldest[e.Name], now.Sub(f.Since).Hours()/24)
			}
		}
	}
	b.WriteString("# HELP goliash_vulnerabilities Known vulnerabilities in running images per environment, severity and whether CISA lists them as exploited.\n")
	b.WriteString("# TYPE goliash_vulnerabilities gauge\n")
	for _, env := range envs {
		for _, sev := range []string{"critical", "high", "medium", "low", "unknown"} {
			for _, x := range []bool{true, false} {
				fmt.Fprintf(b, "goliash_vulnerabilities{environment=%q,severity=%q,exploited=\"%t\"} %d\n", env, sev, x, counts[count{env, sev, x}])
			}
		}
	}
	b.WriteString("# HELP goliash_vulnerabilities_past_due Exploited vulnerabilities still running after the due date CISA set for fixing them (BOD 22-01).\n")
	b.WriteString("# TYPE goliash_vulnerabilities_past_due gauge\n")
	for _, env := range envs {
		fmt.Fprintf(b, "goliash_vulnerabilities_past_due{environment=%q} %d\n", env, pastDue[env])
	}
	b.WriteString("# HELP goliash_exploited_vulnerability_days How long the longest-running exploited vulnerability has run in the environment, in days.\n")
	b.WriteString("# TYPE goliash_exploited_vulnerability_days gauge\n")
	for _, env := range envs {
		fmt.Fprintf(b, "goliash_exploited_vulnerability_days{environment=%q} %.2f\n", env, oldest[env])
	}
}

// rolloutMetrics are the workloads whose replicas are not where they should be: a count per kind, and one series
// per workload while it lasts.
func rolloutMetrics(b *strings.Builder, issues []versions.RolloutIssue) {
	count := map[versions.RolloutKind]int{}
	for _, is := range issues {
		count[is.Kind]++
	}
	b.WriteString("# HELP goliash_rollout_issues Workloads running none of the replicas they ask for (down), two versions side by side for over 30 minutes (stuck), or fewer replicas than asked for (short).\n")
	b.WriteString("# TYPE goliash_rollout_issues gauge\n")
	for _, k := range []versions.RolloutKind{versions.RolloutDown, versions.RolloutStuck, versions.RolloutShort} {
		fmt.Fprintf(b, "goliash_rollout_issues{kind=%q} %d\n", k, count[k])
	}
	b.WriteString("# HELP goliash_rollout_problem 1 for each workload whose replicas are not where they should be, while it lasts.\n")
	b.WriteString("# TYPE goliash_rollout_problem gauge\n")
	for _, is := range issues {
		fmt.Fprintf(b, "goliash_rollout_problem{service=%q,environment=%q,target=%q,workload=%q,kind=%q,running=\"%d\",desired=\"%d\"} 1\n",
			is.Service, is.Environment, is.Target, is.Workload, is.Kind, is.Running, is.Desired)
	}
}

// apiVulnDelta is what a promotion or upgrade does to known vulnerabilities.
type apiVulnDelta struct {
	Fixed []apiVulnRef `json:"fixed"`
	New   []apiVulnRef `json:"new"`
}

type apiVulnRef struct {
	ID        string `json:"id"`
	Severity  string `json:"severity,omitempty"`
	Exploited bool   `json:"exploited"`
}

// toAPIVulnDelta is nil unless both images' SBOMs were read.
func toAPIVulnDelta(d versions.VulnDelta) *apiVulnDelta {
	if !d.Known {
		return nil
	}
	refs := func(vs []versions.VulnRef) []apiVulnRef {
		out := []apiVulnRef{}
		for _, v := range vs {
			out = append(out, apiVulnRef{v.ID, v.Severity, v.Exploited})
		}
		return out
	}
	return &apiVulnDelta{Fixed: refs(d.Fixed), New: refs(d.New)}
}

// gitOpsMetrics describes the GitOps applications (those in trouble, held by a sync window or suspended, synced by
// hand) and the pull requests Goliash opened in the last 30 days: by state, those merged whose version does not run,
// and the medians from opening and from merging to running.
func (h *PublicHandler) gitOpsMetrics(ctx context.Context, b *strings.Builder, sc store.Scope, o versions.Overview, now time.Time) {
	apps, err := versions.GitOpsApps(ctx, h.store, sc, o)
	if err != nil || len(apps) == 0 {
		return
	}
	labels := func(a versions.GitOpsApp) string {
		return fmt.Sprintf("service=%q,environment=%q,target=%q,app=%q,tool=%q", a.Service, a.Environment, a.Target, a.App, a.Tool)
	}
	byTool := map[string]int{}
	var trouble, blocked, manual []string
	for _, a := range apps {
		byTool[a.Tool]++
		if t := a.Trouble(now); t != "" {
			trouble = append(trouble, fmt.Sprintf("goliash_gitops_trouble{%s,trouble=%q} 1", labels(a), t))
		}
		if a.Blocked(now) {
			blocked = append(blocked, fmt.Sprintf("goliash_gitops_sync_blocked{%s} 1", labels(a)))
		}
		if a.ManualSync {
			manual = append(manual, fmt.Sprintf("goliash_gitops_manual_sync{%s} 1", labels(a)))
		}
	}
	b.WriteString("# HELP goliash_gitops_applications GitOps applications deploying workloads, per tool (argocd, flux, git labels).\n# TYPE goliash_gitops_applications gauge\n")
	for _, tool := range []string{"argocd", "flux", "git"} {
		fmt.Fprintf(b, "goliash_gitops_applications{tool=%q} %d\n", tool, byTool[tool])
	}
	for _, m := range []struct {
		head  string
		lines []string
	}{
		{"# HELP goliash_gitops_trouble 1 for each GitOps application out of sync, degraded or missing, while it lasts.\n# TYPE goliash_gitops_trouble gauge\n", trouble},
		{"# HELP goliash_gitops_sync_blocked 1 for each GitOps application an Argo CD sync window holds or Flux has suspended.\n# TYPE goliash_gitops_sync_blocked gauge\n", blocked},
		{"# HELP goliash_gitops_manual_sync 1 for each Argo CD application without an automated sync policy: merging does not deploy it.\n# TYPE goliash_gitops_manual_sync gauge\n", manual},
	} {
		b.WriteString(m.head)
		for _, l := range m.lines {
			b.WriteString(l + "\n")
		}
	}
	pulls, err := versions.PullTimings(ctx, h.store, sc, apps, now.Add(-30*24*time.Hour))
	if err != nil {
		return
	}
	states := map[string]int{"open": 0, "merged": 0, "closed": 0, "undeployed": 0}
	var toRunning, mergeToRunning []time.Duration
	for _, p := range pulls {
		states[p.State]++
		if p.Undeployed() {
			states["undeployed"]++
		}
		toRunning, mergeToRunning = append(toRunning, p.ToRunning), append(mergeToRunning, p.MergeToRunning)
	}
	b.WriteString("# HELP goliash_pull_requests Pull requests Goliash opened in the last 30 days by state; undeployed: merged, and the version does not run yet.\n# TYPE goliash_pull_requests gauge\n")
	for _, s := range []string{"open", "merged", "closed", "undeployed"} {
		fmt.Fprintf(b, "goliash_pull_requests{state=%q} %d\n", s, states[s])
	}
	if d, n := versions.MedianDuration(toRunning); n > 0 {
		b.WriteString("# HELP goliash_pull_request_to_running_seconds Median time from opening a pull request to its version running, last 30 days.\n# TYPE goliash_pull_request_to_running_seconds gauge\n")
		fmt.Fprintf(b, "goliash_pull_request_to_running_seconds %.0f\n", d.Seconds())
	}
	if d, n := versions.MedianDuration(mergeToRunning); n > 0 {
		b.WriteString("# HELP goliash_merge_to_running_seconds Median time from merging a pull request to its version running, last 30 days.\n# TYPE goliash_merge_to_running_seconds gauge\n")
		fmt.Fprintf(b, "goliash_merge_to_running_seconds %.0f\n", d.Seconds())
	}
}
