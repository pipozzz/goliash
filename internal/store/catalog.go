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
	Kind          string // own or third_party
	Upstream      string // image repository releases are read from
	VersionPolicy json.RawMessage
	CreatedAt     time.Time
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

// UpdateService changes a service's owner, kind, upstream and version policy.
func (s *Store) UpdateService(ctx context.Context, svc Service) error {
	if len(svc.VersionPolicy) == 0 {
		svc.VersionPolicy = json.RawMessage(`{}`)
	}
	res, err := s.exec(ctx, s.db, `UPDATE services SET owner = ?, kind = ?, upstream = ?, version_policy = ?
		WHERE org_id = ? AND workspace_id = ? AND id = ?`,
		svc.Owner, svc.Kind, svc.Upstream, string(svc.VersionPolicy), svc.Scope.OrgID, svc.Scope.WorkspaceID, svc.ID)
	return expectOne(res, err)
}

const serviceColumns = `id, org_id, workspace_id, name, owner, kind, upstream, version_policy, created_at`

func scanService(row scanner) (Service, error) {
	var svc Service
	var policy string
	if err := row.Scan(&svc.ID, &svc.Scope.OrgID, &svc.Scope.WorkspaceID, &svc.Name, &svc.Owner, &svc.Kind,
		&svc.Upstream, &policy, &svc.CreatedAt); err != nil {
		return Service{}, notFound(err)
	}
	svc.VersionPolicy, svc.CreatedAt = json.RawMessage(policy), svc.CreatedAt.UTC()
	return svc, nil
}

// MappingRule maps observed instances to a service, or ignores them.
type MappingRule struct {
	ID        string
	Scope     Scope
	Priority  int    // lower runs first
	MatchType string // image_repo, workload_name, label or ignore
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
