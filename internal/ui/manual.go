// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package ui

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/pipozzz/goliash/internal/auth"
	"github.com/pipozzz/goliash/internal/store"
	"github.com/pipozzz/goliash/internal/versions"
	"github.com/pipozzz/goliash/pkg/agentproto"
)

// SnapshotSink stores a snapshot for a target without agent and has it processed like any other;
// the ingest service is one. Without one, the UI stores the snapshot and the processor finds it later.
type SnapshotSink interface {
	LocalSnapshot(ctx context.Context, t store.Target, snap agentproto.Snapshot) (bool, error)
}

// ManualEntry is a version people entered for software Goliash does not collect.
type ManualEntry struct {
	Env, Where, Version string
}

// NewServiceView is the form for a service entered by hand.
type NewServiceView struct {
	Base
	Envs []string
	Form url.Values
}

func (s *Server) newService(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	return s.showNewService(w, r, p, url.Values{}, "")
}

func (s *Server) showNewService(w http.ResponseWriter, r *http.Request, p auth.Principal, form url.Values, problem string) error {
	envs, err := s.store.ListEnvironments(r.Context(), p.Scope)
	if err != nil {
		return err
	}
	v := NewServiceView{Base: s.base(r.Context(), p, "matrix", "Add a service by hand"), Form: form}
	v.Error = problem
	for _, e := range envs {
		v.Envs = append(v.Envs, e.Name)
	}
	return render(w, r, NewServicePage(v))
}

// createService adds a service by hand: watched for its releases, and optionally running somewhere
// Goliash does not collect.
func (s *Server) createService(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	ctx := r.Context()
	if err := r.ParseForm(); err != nil {
		return err
	}
	name := strings.TrimSpace(r.PostForm.Get("name"))
	upstream := strings.TrimSpace(r.PostForm.Get("upstream"))
	switch {
	case name == "" || len(name) > 100 || strings.ContainsAny(name, "/?#"):
		return s.showNewService(w, r, p, r.PostForm, "Give the service a name of at most 100 characters, without / ? #.")
	case upstream == "":
		return s.showNewService(w, r, p, r.PostForm, "Give the image repository its releases are read from, e.g. docker.io/library/postgres.")
	}
	if _, err := s.store.GetServiceByName(ctx, p.Scope, name); err == nil {
		return s.showNewService(w, r, p, r.PostForm, "A service named "+name+" exists already; open it and enter where it runs there.")
	}
	svc, err := s.store.EnsureService(ctx, p.Scope, name)
	if err != nil {
		return err
	}
	svc.Upstream = versions.ParseImage(upstream).Repo()
	svc.Owner = strings.TrimSpace(r.PostForm.Get("owner"))
	if strings.HasPrefix(svc.Upstream, "docker.io/") || strings.Contains(svc.Upstream, ".") {
		svc.Kind = "third_party"
	}
	if err := s.store.UpdateService(ctx, svc); err != nil {
		return err
	}
	s.audit(ctx, p, "service.create", "service", name, "upstream", svc.Upstream)
	if version := strings.TrimSpace(r.PostForm.Get("version")); version != "" {
		if problem, err := s.setManual(ctx, p, svc, r.PostForm.Get("env"), r.PostForm.Get("where"), version); err != nil {
			return err
		} else if problem != "" {
			return back(w, r, serviceURL(name), "error", problem)
		}
	}
	if s.checker != nil {
		// Its releases now, so the page it opens on already shows them (and any drift they open).
		_ = s.checker.CheckService(ctx, p.Scope, svc.ID)
		s.hub.Publish(p.Scope.WorkspaceID)
	}
	return back(w, r, serviceURL(name), "notice", name+" added. Goliash checks "+svc.Upstream+" for new releases.")
}

// setManualVersion enters, changes or (with an empty version) removes where a service runs outside Goliash.
func (s *Server) setManualVersion(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	svc, err := s.store.GetServiceByName(r.Context(), p.Scope, r.PathValue("name"))
	if err != nil {
		return err
	}
	problem, err := s.setManual(r.Context(), p, svc, r.FormValue("env"), r.FormValue("where"), strings.TrimSpace(r.FormValue("version")))
	if err != nil {
		return err
	}
	if problem != "" {
		return back(w, r, serviceURL(svc.Name), "error", problem)
	}
	return back(w, r, serviceURL(svc.Name), "notice", "Saved. The matrix shows it within a few seconds.")
}

// setManual records version as running for svc on the manual target where in env, as a snapshot of every
// manual entry on that target, so history, drift and notifications follow as for collected targets.
func (s *Server) setManual(ctx context.Context, p auth.Principal, svc store.Service, envName, where, version string) (string, error) {
	where = strings.TrimSpace(where)
	if where == "" || len(where) > 100 {
		return "Say where it runs, e.g. the host or the managed service (at most 100 characters).", nil
	}
	if len(version) > 128 || strings.ContainsAny(version, " @/") {
		return "Enter the version as a tag, e.g. 16.4.", nil
	}
	env, err := s.store.GetEnvironmentByName(ctx, p.Scope, envName)
	if errors.Is(err, store.ErrNotFound) {
		return "Choose an environment.", nil
	} else if err != nil {
		return "", err
	}
	t, err := s.manualTarget(ctx, p.Scope, env, where)
	if err != nil || t.ID == "" {
		return "There is already a collected target named " + where + "; choose another name for where it runs.", err
	}
	insts, err := s.store.ListTargetInstances(ctx, p.Scope, t.ID)
	if err != nil {
		return "", err
	}
	repo := svc.Upstream
	if repo == "" {
		repo = svc.Name
	}
	key := "manual/" + svc.Name
	byID := map[string]agentproto.Workload{}
	for _, in := range insts {
		if !in.RemovedAt.IsZero() || in.WorkloadID == key {
			continue
		}
		byID[in.WorkloadID] = manualWorkload(in.WorkloadID, in.WorkloadName, in.Image)
	}
	if version != "" {
		byID[key] = manualWorkload(key, svc.Name, repo+":"+version)
	}
	ids := make([]string, 0, len(byID))
	for id := range byID {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	snap := agentproto.Snapshot{SnapshotID: store.NewID(), TargetID: t.ID, CollectedAt: time.Now().UTC(), Complete: true}
	for _, id := range ids {
		snap.Workloads = append(snap.Workloads, byID[id])
	}
	if err := s.storeSnapshot(ctx, t, snap); err != nil {
		return "", err
	}
	s.audit(ctx, p, "service.manual_version", "service", svc.Name, "environment", env.Name, "where", where, "version", orDash(version))
	return "", nil
}

func manualWorkload(id, service, image string) agentproto.Workload {
	return agentproto.Workload{
		ID: id, Kind: agentproto.ManualEntry, Name: service, Labels: map[string]string{"goliash.service": service},
		Containers: []agentproto.Container{{Name: service, Image: image, Running: 1}},
	}
}

// manualTarget finds or creates the manual target named where in env; a collected target of that name
// gives an empty target.
func (s *Server) manualTarget(ctx context.Context, sc store.Scope, env store.Environment, where string) (store.Target, error) {
	targets, err := s.store.ListTargets(ctx, sc)
	if err != nil {
		return store.Target{}, err
	}
	for _, t := range targets {
		if t.Name == where {
			if !versions.Manual(t.Platform) || t.EnvironmentID != env.ID {
				return store.Target{}, nil
			}
			return t, nil
		}
	}
	return s.store.CreateTarget(ctx, store.Target{
		Scope: sc, EnvironmentID: env.ID, Platform: string(agentproto.Manual), Name: where,
		Settings: []byte(`{}`), PollIntervalSeconds: 24 * 3600,
	})
}

func (s *Server) storeSnapshot(ctx context.Context, t store.Target, snap agentproto.Snapshot) error {
	if s.snapshots != nil {
		_, err := s.snapshots.LocalSnapshot(ctx, t, snap)
		return err
	}
	raw, err := json.Marshal(snap)
	if err != nil {
		return err
	}
	_, err = s.store.InsertSnapshot(ctx, store.Snapshot{
		ID: snap.SnapshotID, Scope: t.Scope, TargetID: t.ID, CollectedAt: snap.CollectedAt, Complete: true, Payload: raw,
	})
	return err
}

// manualEntries lists where a service runs by hand, for its page.
func (s *Server) manualEntries(ctx context.Context, sc store.Scope, svc store.Service, envName map[string]string) ([]ManualEntry, error) {
	targets, err := s.store.ListTargets(ctx, sc)
	if err != nil {
		return nil, err
	}
	var out []ManualEntry
	for _, t := range targets {
		if !versions.Manual(t.Platform) {
			continue
		}
		insts, err := s.store.ListTargetInstances(ctx, sc, t.ID)
		if err != nil {
			return nil, err
		}
		for _, in := range insts {
			if in.RemovedAt.IsZero() && in.WorkloadID == "manual/"+svc.Name {
				out = append(out, ManualEntry{Env: envName[t.EnvironmentID], Where: t.Name, Version: in.Tag})
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Env+out[i].Where < out[j].Env+out[j].Where })
	return out, nil
}
