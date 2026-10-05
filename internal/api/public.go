// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"context"
	"encoding/csv"
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
	"github.com/pipozzz/goliash/pkg/buildinfo"
)

// PublicHandler serves the REST API under /api/v1 and Prometheus metrics.
type PublicHandler struct {
	store    *store.Store
	auth     *auth.Auth
	log      *slog.Logger
	isLeader func() bool
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
	mux.Handle("GET /api/v1/environments", h.with(store.RoleViewer, h.environments))
	mux.Handle("GET /api/v1/targets", h.with(store.RoleViewer, h.targets))
	mux.Handle("GET /api/v1/events", h.with(store.RoleViewer, h.events))
	mux.Handle("GET /api/v1/drifts", h.with(store.RoleViewer, h.drifts))
	mux.Handle("GET /api/v1/promotions", h.with(store.RoleViewer, h.promotions))
	mux.Handle("GET /api/v1/delivery", h.with(store.RoleViewer, h.delivery))
	mux.Handle("GET /api/v1/inventory", h.with(store.RoleViewer, h.inventory))
	mux.Handle("GET /api/v1/hygiene", h.with(store.RoleViewer, h.hygiene))
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
			ae := apiEnv{Environment: e.Env.Name, Deploys: e.Deploys}
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
	if r.URL.Query().Get("format") != "csv" {
		writeJSON(w, http.StatusOK, items)
		return
	}
	name := "goliash-inventory.csv"
	if at != nil {
		name = "goliash-inventory-" + at.UTC().Format("20060102-1504") + ".csv"
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	cw := csv.NewWriter(w)
	_ = cw.Write([]string{"environment", "target", "platform", "service", "namespace", "workload", "kind", "container", "image", "tag", "digest", "running", "first_seen"})
	for _, i := range items {
		_ = cw.Write([]string{
			i.Environment, i.Target, i.Platform, i.Service, i.Namespace, i.Workload, i.Kind, i.Container,
			i.Image, i.Tag, i.Digest, strconv.Itoa(i.Running), i.FirstSeen.UTC().Format(time.RFC3339),
		})
	}
	cw.Flush()
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
		byKind := map[string]int{"moving-tag": 0, "retagged": 0, "untrusted-registry": 0, "unpinned": 0}
		for _, f := range findings {
			byKind[f.Kind]++
		}
		b.WriteString("# HELP goliash_image_hygiene_findings Images with a moving tag, a tag pushed again, an untrusted registry or no digest.\n")
		b.WriteString("# TYPE goliash_image_hygiene_findings gauge\n")
		for _, k := range []string{"moving-tag", "retagged", "untrusted-registry", "unpinned"} {
			fmt.Fprintf(&b, "goliash_image_hygiene_findings{kind=%q} %d\n", k, byKind[k])
		}
	}
	h.serverMetrics(r.Context(), &b, p.Scope)
	b.WriteString("# HELP goliash_drift_days How long a drift has been open, in days.\n")
	b.WriteString("# TYPE goliash_drift_days gauge\n")
	for _, l := range days {
		b.WriteString(l + "\n")
	}
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	_, _ = w.Write([]byte(b.String()))
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
