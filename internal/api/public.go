// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	spec "github.com/pipozzz/goliash/api"
	"github.com/pipozzz/goliash/internal/auth"
	"github.com/pipozzz/goliash/internal/store"
	"github.com/pipozzz/goliash/internal/versions"
)

// PublicHandler serves the REST API under /api/v1 and Prometheus metrics.
type PublicHandler struct {
	store *store.Store
	auth  *auth.Auth
	log   *slog.Logger
}

// NewPublicHandler returns the public API handler.
func NewPublicHandler(st *store.Store, a *auth.Auth, log *slog.Logger) *PublicHandler {
	return &PublicHandler{store: st, auth: a, log: log}
}

// Register adds the routes to mux.
func (h *PublicHandler) Register(mux *http.ServeMux) {
	mux.Handle("GET /api/v1/matrix", h.with(store.RoleViewer, h.matrix))
	mux.Handle("GET /api/v1/services", h.with(store.RoleViewer, h.services))
	mux.Handle("GET /api/v1/environments", h.with(store.RoleViewer, h.environments))
	mux.Handle("GET /api/v1/targets", h.with(store.RoleViewer, h.targets))
	mux.Handle("GET /api/v1/events", h.with(store.RoleViewer, h.events))
	mux.Handle("GET /api/v1/drifts", h.with(store.RoleViewer, h.drifts))
	mux.Handle("GET /api/v1/promotions", h.with(store.RoleViewer, h.promotions))
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
	o, err := versions.LoadOverview(r.Context(), h.store, p.Scope)
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
		Service: o.Services[d.ServiceID].Name, Environment: o.Envs[d.EnvironmentID].Name, Kind: d.Kind,
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
	out := []apiService{}
	for _, s := range svcs {
		out = append(out, apiService{ID: s.ID, Name: s.Name, Owner: s.Owner, Kind: s.Kind, Upstream: s.Upstream, Policy: s.VersionPolicy})
	}
	writeJSON(w, http.StatusOK, out)
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
			ID: e.ID, Type: e.Type, Service: o.Services[e.ServiceID].Name, Environment: o.Envs[e.EnvironmentID].Name,
			Target: targetName[e.TargetID], From: e.FromVersion, To: e.ToVersion, Note: e.Note, Source: e.Source, At: e.At,
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
	type apiRelease struct {
		Version      string     `json:"version"`
		PublishedAt  *time.Time `json:"published_at,omitempty"`
		ReleaseNotes string     `json:"release_notes,omitempty"`
	}
	type apiPromotion struct {
		Service  string       `json:"service"`
		From     string       `json:"from"`
		To       string       `json:"to"`
		Version  string       `json:"version"`
		Running  string       `json:"running"`
		Since    time.Time    `json:"since"`
		Releases []apiRelease `json:"releases"`
	}
	out := []apiPromotion{}
	for _, pr := range list {
		ap := apiPromotion{Service: pr.Service.Name, From: pr.From.Name, To: pr.To.Name, Version: pr.Version, Running: pr.Running, Since: pr.Since, Releases: []apiRelease{}}
		for _, rel := range pr.Releases {
			ar := apiRelease{Version: rel.Version, ReleaseNotes: rel.ChangelogURL}
			if !rel.PublishedAt.IsZero() {
				t := rel.PublishedAt
				ar.PublishedAt = &t
			}
			ap.Releases = append(ap.Releases, ar)
		}
		out = append(out, ap)
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
				days = append(days, fmt.Sprintf("goliash_drift_days{service=%q,environment=%q,kind=%q} %.2f",
					row.Service.Name, env.Name, d.Kind, time.Since(d.Since).Hours()/24))
			}
			outdated = append(outdated, fmt.Sprintf("goliash_outdated{service=%q,environment=%q} %d", row.Service.Name, env.Name, isOutdated))
		}
	}
	b.WriteString("# HELP goliash_outdated 1 when the environment runs a version behind upstream by the tracked jump.\n")
	b.WriteString("# TYPE goliash_outdated gauge\n")
	for _, l := range outdated {
		b.WriteString(l + "\n")
	}
	b.WriteString("# HELP goliash_drift_days How long a drift has been open, in days.\n")
	b.WriteString("# TYPE goliash_drift_days gauge\n")
	for _, l := range days {
		b.WriteString(l + "\n")
	}
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	_, _ = w.Write([]byte(b.String()))
}
