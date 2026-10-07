// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

// Service is a logical service, own or third-party, tracked across environments.
type Service struct {
	ID            string
	Scope         Scope
	Name          string
	Owner         string
	OwnerSource   string // '' set by people; "label" or "app" when Goliash set it and follows changes
	App           string // the application it is placed in by hand; empty: from its workloads
	Kind          string // own or third_party
	Upstream      string // image repository releases are read from
	VersionPolicy json.RawMessage
	CreatedAt     time.Time

	// SourceURL is the source repository the upstream image SourceImage declares,
	// read at SourceCheckedAt; empty when it declares none.
	SourceURL       string
	SourceImage     string
	SourceCheckedAt time.Time

	// PrivateUpstream is the upstream repository a public registry refused to show
	// anonymously; while it equals the upstream, the agents check it.
	PrivateUpstream string
}

// EnsureService returns the workspace's service with the given name, creating it
// (kind "own") when it does not exist.
func (s *Store) EnsureService(ctx context.Context, sc Scope, name string) (Service, error) {
	svc, err := s.GetServiceByName(ctx, sc, name)
	if !errors.Is(err, ErrNotFound) {
		return svc, err
	}
	svc = Service{ID: NewID(), Scope: sc, Name: name, Kind: "own", VersionPolicy: json.RawMessage(`{}`), CreatedAt: s.now()}
	_, err = s.exec(ctx, s.db, `
		INSERT INTO services (id, org_id, workspace_id, name, kind, created_at) VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT (workspace_id, name) DO NOTHING`,
		svc.ID, sc.OrgID, sc.WorkspaceID, name, svc.Kind, svc.CreatedAt)
	if err != nil {
		return Service{}, err
	}
	return s.GetServiceByName(ctx, sc, name) // the winner if another writer raced us
}

// GetServiceByName returns a service of the workspace.
func (s *Store) GetServiceByName(ctx context.Context, sc Scope, name string) (Service, error) {
	return scanService(s.queryRow(ctx, s.db, `SELECT `+serviceColumns+` FROM services
		WHERE org_id = ? AND workspace_id = ? AND name = ?`, sc.OrgID, sc.WorkspaceID, name))
}

// GetService returns a service of the workspace by ID.
func (s *Store) GetService(ctx context.Context, sc Scope, id string) (Service, error) {
	return scanService(s.queryRow(ctx, s.db, `SELECT `+serviceColumns+` FROM services
		WHERE org_id = ? AND workspace_id = ? AND id = ?`, sc.OrgID, sc.WorkspaceID, id))
}

// ListServices returns the workspace's services by name.
func (s *Store) ListServices(ctx context.Context, sc Scope) ([]Service, error) {
	rows, err := s.query(ctx, s.db, `SELECT `+serviceColumns+` FROM services
		WHERE org_id = ? AND workspace_id = ? ORDER BY name`, sc.OrgID, sc.WorkspaceID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []Service
	for rows.Next() {
		svc, err := scanService(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, svc)
	}
	return out, rows.Err()
}

// SetServiceSource records the source repository image declares (empty for none).
func (s *Store) SetServiceSource(ctx context.Context, sc Scope, id, image, source string) error {
	res, err := s.exec(ctx, s.db, `UPDATE services SET source_url = ?, source_image = ?, source_checked_at = ?
		WHERE org_id = ? AND workspace_id = ? AND id = ?`, source, image, s.now(), sc.OrgID, sc.WorkspaceID, id)
	return expectOne(res, err)
}

// SetPrivateUpstream records that repo, a service's upstream on a public registry,
// needs credentials (empty: it does not).
func (s *Store) SetPrivateUpstream(ctx context.Context, sc Scope, id, repo string) error {
	res, err := s.exec(ctx, s.db, `UPDATE services SET private_upstream = ? WHERE org_id = ? AND workspace_id = ? AND id = ?`,
		repo, sc.OrgID, sc.WorkspaceID, id)
	return expectOne(res, err)
}

// UpdateService changes a service's owner, kind, upstream and version policy.
func (s *Store) UpdateService(ctx context.Context, svc Service) error {
	if len(svc.VersionPolicy) == 0 {
		svc.VersionPolicy = json.RawMessage(`{}`)
	}
	// An owner changed here is set by people: Goliash no longer follows labels for it.
	res, err := s.exec(ctx, s.db, `UPDATE services SET owner_source = CASE WHEN owner = ? THEN owner_source ELSE '' END,
		owner = ?, kind = ?, upstream = ?, version_policy = ?
		WHERE org_id = ? AND workspace_id = ? AND id = ?`,
		svc.Owner, svc.Owner, svc.Kind, svc.Upstream, string(svc.VersionPolicy), svc.Scope.OrgID, svc.Scope.WorkspaceID, svc.ID)
	return expectOne(res, err)
}

const serviceColumns = `id, org_id, workspace_id, name, owner, kind, upstream, version_policy, created_at,
	source_url, source_image, source_checked_at, private_upstream, app, owner_source`

func scanService(row scanner) (Service, error) {
	var svc Service
	var policy string
	var checked sql.NullTime
	if err := row.Scan(&svc.ID, &svc.Scope.OrgID, &svc.Scope.WorkspaceID, &svc.Name, &svc.Owner, &svc.Kind,
		&svc.Upstream, &policy, &svc.CreatedAt, &svc.SourceURL, &svc.SourceImage, &checked, &svc.PrivateUpstream, &svc.App, &svc.OwnerSource); err != nil {
		return Service{}, notFound(err)
	}
	svc.VersionPolicy, svc.CreatedAt = json.RawMessage(policy), svc.CreatedAt.UTC()
	svc.SourceCheckedAt = timeOrZero(checked)
	return svc, nil
}

// MappingRule maps observed instances to a service, or ignores them.
type MappingRule struct {
	ID        string
	Scope     Scope
	Priority  int    // lower runs first
	MatchType string // image_repo, workload_name, app_workload ("<app>/<workload>"), label or ignore
	Pattern   string // regular expression; for label: key=value-regexp
	ServiceID string // empty for ignore rules
	CreatedAt time.Time
}

// CreateMappingRule adds a rule. ID and CreatedAt are set by the store.
func (s *Store) CreateMappingRule(ctx context.Context, r MappingRule) (MappingRule, error) {
	r.ID, r.CreatedAt = NewID(), s.now()
	_, err := s.exec(ctx, s.db, `INSERT INTO mapping_rules (id, org_id, workspace_id, priority, match_type, pattern,
		service_id, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		r.ID, r.Scope.OrgID, r.Scope.WorkspaceID, r.Priority, r.MatchType, r.Pattern, nullString(r.ServiceID), r.CreatedAt)
	return r, err
}

// ListMappingRules returns the workspace's rules in evaluation order.
func (s *Store) ListMappingRules(ctx context.Context, sc Scope) ([]MappingRule, error) {
	rows, err := s.query(ctx, s.db, `SELECT id, priority, match_type, pattern, service_id, created_at FROM mapping_rules
		WHERE org_id = ? AND workspace_id = ? ORDER BY priority, created_at, id`, sc.OrgID, sc.WorkspaceID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []MappingRule
	for rows.Next() {
		r := MappingRule{Scope: sc}
		var svc sql.NullString
		if err := rows.Scan(&r.ID, &r.Priority, &r.MatchType, &r.Pattern, &svc, &r.CreatedAt); err != nil {
			return nil, err
		}
		r.ServiceID, r.CreatedAt = svc.String, r.CreatedAt.UTC()
		out = append(out, r)
	}
	return out, rows.Err()
}
