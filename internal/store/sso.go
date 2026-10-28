// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"context"
	"errors"
	"time"
)

// ErrSSOUnencrypted refuses to store a client secret while the server has no secret key.
var ErrSSOUnencrypted = errors.New("the server has no secret key to encrypt the client secret with (GOLIASH_SECRET_KEY)")

// SSOSettings is an organization's single sign-on, set up in Goliash: an OpenID Connect provider and the roles its
// groups give. Lists are comma-separated, as the environment variables they stand in for.
type SSOSettings struct {
	OrgID        string
	Issuer       string
	ClientID     string
	ClientSecret string
	Name         string // the sign-in button's label
	Domains      string // e-mail domains whose people get a viewer account on first sign-in
	GroupsClaim  string
	Scopes       string
	GroupRoles   string // "goliash-admins=admin, sre=member@payments"
	UpdatedBy    string
	UpdatedAt    time.Time
}

// GetSSO returns the organization's single sign-on; ErrNotFound when it has none.
func (s *Store) GetSSO(ctx context.Context, orgID string) (SSOSettings, error) {
	v := SSOSettings{OrgID: orgID}
	var secret string
	err := s.queryRow(ctx, s.db, `SELECT issuer, client_id, client_secret, name, domains, groups_claim, scopes, group_roles,
		updated_by, updated_at FROM sso_settings WHERE org_id = ?`, orgID).
		Scan(&v.Issuer, &v.ClientID, &secret, &v.Name, &v.Domains, &v.GroupsClaim, &v.Scopes, &v.GroupRoles, &v.UpdatedBy, &v.UpdatedAt)
	if err != nil {
		return v, notFound(err)
	}
	v.ClientSecret, err = s.open("sso:"+orgID, secret)
	return v, err
}

// SetSSO saves the organization's single sign-on, sealing the client secret.
func (s *Store) SetSSO(ctx context.Context, v SSOSettings) error {
	if !s.Encrypts() {
		return ErrSSOUnencrypted
	}
	_, err := s.exec(ctx, s.db, `INSERT INTO sso_settings (org_id, issuer, client_id, client_secret, name, domains, groups_claim,
		scopes, group_roles, updated_by, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (org_id) DO UPDATE SET issuer = excluded.issuer, client_id = excluded.client_id,
		client_secret = excluded.client_secret, name = excluded.name, domains = excluded.domains,
		groups_claim = excluded.groups_claim, scopes = excluded.scopes, group_roles = excluded.group_roles,
		updated_by = excluded.updated_by, updated_at = excluded.updated_at`,
		v.OrgID, v.Issuer, v.ClientID, s.seal("sso:"+v.OrgID, v.ClientSecret), v.Name, v.Domains, v.GroupsClaim, v.Scopes,
		v.GroupRoles, v.UpdatedBy, s.now())
	return err
}

// DeleteSSO removes the organization's single sign-on.
func (s *Store) DeleteSSO(ctx context.Context, orgID string) error {
	_, err := s.exec(ctx, s.db, `DELETE FROM sso_settings WHERE org_id = ?`, orgID)
	return err
}
