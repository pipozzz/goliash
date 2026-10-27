// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/pipozzz/goliash/internal/store"
)

// GroupRoles maps the groups of an identity provider (from OIDC's groups claim or SCIM) to roles: admin for the
// whole organization, or member and viewer in every workspace or one. With it, the identity provider decides who
// may sign in and with which role, at every sign-in.
type GroupRoles []GroupRole

// GroupRole is one entry: people in Group get Role, in Workspace (a slug or name) or, when empty, in every one.
type GroupRole struct {
	Group, Role, Workspace string
}

// ErrNoGroup is returned for a person in no group GroupRoles names.
var ErrNoGroup = errors.New("not in any group that gives a role in Goliash")

// ParseGroupRoles reads "goliash-admins=admin, sre=member, sre-pay=member@payments, everyone=viewer".
func ParseGroupRoles(s string) (GroupRoles, error) {
	var out GroupRoles
	for _, entry := range strings.Split(s, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		group, grant, ok := strings.Cut(entry, "=")
		role, ws, _ := strings.Cut(strings.TrimSpace(grant), "@")
		group, role, ws = strings.TrimSpace(group), strings.ToLower(strings.TrimSpace(role)), strings.TrimSpace(ws)
		switch {
		case !ok || group == "":
			return nil, fmt.Errorf("group roles: %q is not group=role", entry)
		case role != store.RoleAdmin && role != store.RoleMember && role != store.RoleViewer:
			return nil, fmt.Errorf("group roles: %q: the role is admin, member or viewer", entry)
		case role == store.RoleAdmin && ws != "":
			return nil, fmt.Errorf("group roles: %q: admin is for the whole organization", entry)
		}
		out = append(out, GroupRole{Group: group, Role: role, Workspace: ws})
	}
	return out, nil
}

// Grants is what a person's groups give: their organization role, and their role in each workspace ("" for every
// workspace). ok is false when no group gives anything.
type Grants struct {
	OrgRole    string
	Workspaces map[string]string
}

// For returns the grants of a person in groups. A group matches by name, also when the provider writes it as a path
// (Keycloak's /sre).
func (g GroupRoles) For(groups []string) (Grants, bool) {
	in := map[string]bool{}
	for _, gr := range groups {
		in[gr], in[strings.TrimPrefix(gr, "/")] = true, true
	}
	out := Grants{Workspaces: map[string]string{}}
	for _, e := range g {
		if !in[e.Group] {
			continue
		}
		if store.RoleRank(e.Role) > store.RoleRank(out.OrgRole) {
			out.OrgRole = e.Role
		}
		if e.Role != store.RoleAdmin && store.RoleRank(e.Role) > store.RoleRank(out.Workspaces[e.Workspace]) {
			out.Workspaces[e.Workspace] = e.Role
		}
	}
	return out, out.OrgRole != ""
}

// ApplyGrants gives u what grants say, taking away what they no longer say: the organization role, and a membership
// in each workspace with the higher of the role for it and the role for every workspace. An owner keeps being one.
func ApplyGrants(ctx context.Context, st *store.Store, u store.User, g Grants) error {
	if u.Role == store.RoleOwner {
		return nil
	}
	if u.Role != g.OrgRole {
		if err := st.SetUserRole(ctx, u.OrgID, u.ID, g.OrgRole); err != nil {
			return err
		}
	}
	wss, err := st.ListOrgWorkspaces(ctx, u.OrgID)
	if err != nil {
		return err
	}
	for _, w := range wss {
		role := g.Workspaces[""]
		for _, key := range []string{w.Slug, w.Name} {
			if r := g.Workspaces[key]; store.RoleRank(r) > store.RoleRank(role) {
				role = r
			}
		}
		if role == "" || g.OrgRole == store.RoleAdmin {
			if err := st.RemoveMembership(ctx, u.ID, w.ID); err != nil {
				return err
			}
			continue
		}
		if err := st.SetMembership(ctx, u.ID, w.ID, role); err != nil {
			return err
		}
	}
	return nil
}
