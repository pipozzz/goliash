// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

// Package scim serves SCIM 2.0 (RFC 7643, 7644) at /scim/v2, so an identity provider (Entra ID, Okta, Authentik,
// Keycloak, JumpCloud) creates people, deactivates them when they leave, and pushes the groups that decide their
// roles as GOLIASH_GROUP_ROLES maps them.
package scim

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/pipozzz/goliash/internal/auth"
	"github.com/pipozzz/goliash/internal/store"
)

const (
	userSchema  = "urn:ietf:params:scim:schemas:core:2.0:User"
	groupSchema = "urn:ietf:params:scim:schemas:core:2.0:Group"
	listSchema  = "urn:ietf:params:scim:api:messages:2.0:ListResponse"
	patchSchema = "urn:ietf:params:scim:api:messages:2.0:PatchOp"
	errorSchema = "urn:ietf:params:scim:api:messages:2.0:Error"
	contentType = "application/scim+json"
)

// Handler serves SCIM for the organization of the server's first workspace.
type Handler struct {
	store *store.Store
	token string
	roles auth.GroupRoles
	base  string // the public URL of /scim/v2, for resource locations
	log   *slog.Logger
	mux   *http.ServeMux
}

// New returns the SCIM handler; requests must carry token as a bearer token. roles may be empty: people then get a
// viewer role in the first workspace, and groups are kept without deciding anything.
func New(st *store.Store, token string, roles auth.GroupRoles, publicURL string, log *slog.Logger) *Handler {
	h := &Handler{store: st, token: token, roles: roles, base: strings.TrimSuffix(publicURL, "/") + "/scim/v2", log: log, mux: http.NewServeMux()}
	h.mux.HandleFunc("GET /scim/v2/ServiceProviderConfig", h.serviceProviderConfig)
	h.mux.HandleFunc("GET /scim/v2/ResourceTypes", h.resourceTypes)
	h.mux.HandleFunc("GET /scim/v2/Schemas", h.schemas)
	h.mux.HandleFunc("GET /scim/v2/Users", h.listUsers)
	h.mux.HandleFunc("POST /scim/v2/Users", h.createUser)
	h.mux.HandleFunc("GET /scim/v2/Users/{id}", h.getUser)
	h.mux.HandleFunc("PUT /scim/v2/Users/{id}", h.replaceUser)
	h.mux.HandleFunc("PATCH /scim/v2/Users/{id}", h.patchUser)
	h.mux.HandleFunc("DELETE /scim/v2/Users/{id}", h.deleteUser)
	h.mux.HandleFunc("GET /scim/v2/Groups", h.listGroups)
	h.mux.HandleFunc("POST /scim/v2/Groups", h.createGroup)
	h.mux.HandleFunc("GET /scim/v2/Groups/{id}", h.getGroup)
	h.mux.HandleFunc("PUT /scim/v2/Groups/{id}", h.replaceGroup)
	h.mux.HandleFunc("PATCH /scim/v2/Groups/{id}", h.patchGroup)
	h.mux.HandleFunc("DELETE /scim/v2/Groups/{id}", h.deleteGroup)
	return h
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	got, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || subtle.ConstantTimeCompare([]byte(strings.TrimSpace(got)), []byte(h.token)) != 1 {
		fail(w, http.StatusUnauthorized, "", "a valid bearer token is required")
		return
	}
	h.mux.ServeHTTP(w, r)
}

// org is the organization SCIM provisions: the one of the first workspace, as for OIDC.
func (h *Handler) org(ctx context.Context) (string, error) {
	ws, err := h.store.ListWorkspaces(ctx)
	if err != nil || len(ws) == 0 {
		return "", errors.New("no organization")
	}
	return ws[0].OrgID, nil
}

// ---- users ----

type name struct {
	Formatted  string `json:"formatted,omitempty"`
	GivenName  string `json:"givenName,omitempty"`
	FamilyName string `json:"familyName,omitempty"`
}

type multi struct {
	Value   string `json:"value"`
	Display string `json:"display,omitempty"`
	Type    string `json:"type,omitempty"`
	Primary bool   `json:"primary,omitempty"`
	Ref     string `json:"$ref,omitempty"`
}

type meta struct {
	ResourceType string    `json:"resourceType"`
	Created      time.Time `json:"created"`
	Location     string    `json:"location"`
}

type userResource struct {
	Schemas     []string `json:"schemas"`
	ID          string   `json:"id,omitempty"`
	ExternalID  string   `json:"externalId,omitempty"`
	UserName    string   `json:"userName"`
	Name        *name    `json:"name,omitempty"`
	DisplayName string   `json:"displayName,omitempty"`
	Emails      []multi  `json:"emails,omitempty"`
	Active      *bool    `json:"active,omitempty"`
	Groups      []multi  `json:"groups,omitempty"`
	Meta        *meta    `json:"meta,omitempty"`
}

// email is the person's address: userName when it is one, else the primary (or first) e-mail.
func (u userResource) email() string {
	if strings.Contains(u.UserName, "@") {
		return u.UserName
	}
	for _, e := range u.Emails {
		if e.Primary {
			return e.Value
		}
	}
	if len(u.Emails) > 0 {
		return u.Emails[0].Value
	}
	return ""
}

func (u userResource) fullName() string {
	switch {
	case u.DisplayName != "":
		return u.DisplayName
	case u.Name != nil && u.Name.Formatted != "":
		return u.Name.Formatted
	case u.Name != nil:
		return strings.TrimSpace(u.Name.GivenName + " " + u.Name.FamilyName)
	}
	return ""
}

func (h *Handler) userResource(ctx context.Context, u store.User) userResource {
	active := !u.Disabled
	out := userResource{
		Schemas: []string{userSchema}, ID: u.ID, ExternalID: u.ExternalID, UserName: u.Email, DisplayName: u.Name,
		Emails: []multi{{Value: u.Email, Type: "work", Primary: true}}, Active: &active,
		Meta: &meta{ResourceType: "User", Created: u.CreatedAt, Location: h.base + "/Users/" + u.ID},
	}
	if u.Name != "" {
		out.Name = &name{Formatted: u.Name}
	}
	if groups, err := h.store.UserSCIMGroups(ctx, u.ID); err == nil {
		for _, g := range groups {
			out.Groups = append(out.Groups, multi{Value: g.ID, Display: g.DisplayName, Ref: h.base + "/Groups/" + g.ID})
		}
	}
	return out
}

func (h *Handler) listUsers(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	orgID, err := h.org(ctx)
	if err != nil {
		fail(w, http.StatusInternalServerError, "", err.Error())
		return
	}
	attr, value, err := filter(r.URL.Query().Get("filter"))
	if err != nil {
		fail(w, http.StatusBadRequest, "invalidFilter", err.Error())
		return
	}
	users, err := h.store.ListUsers(ctx, orgID)
	if err != nil {
		fail(w, http.StatusInternalServerError, "", err.Error())
		return
	}
	var found []any
	for _, u := range users {
		switch strings.ToLower(attr) {
		case "":
		case "username", "emails.value", "emails":
			if !strings.EqualFold(u.Email, value) {
				continue
			}
		case "externalid":
			if u.ExternalID != value {
				continue
			}
		case "id":
			if u.ID != value {
				continue
			}
		default:
			fail(w, http.StatusBadRequest, "invalidFilter", "filter on userName, externalId or id")
			return
		}
		found = append(found, h.userResource(ctx, u))
	}
	list(w, r, found)
}

func (h *Handler) getUser(w http.ResponseWriter, r *http.Request) {
	u, ok := h.user(w, r)
	if ok {
		reply(w, http.StatusOK, h.userResource(r.Context(), u))
	}
}

// user is the organization's user the path names.
func (h *Handler) user(w http.ResponseWriter, r *http.Request) (store.User, bool) {
	orgID, err := h.org(r.Context())
	if err != nil {
		fail(w, http.StatusInternalServerError, "", err.Error())
		return store.User{}, false
	}
	u, err := h.store.GetUser(r.Context(), r.PathValue("id"))
	if err != nil || u.OrgID != orgID {
		fail(w, http.StatusNotFound, "", "no such user")
		return store.User{}, false
	}
	return u, true
}

func (h *Handler) createUser(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var in userResource
	if !decode(w, r, &in) {
		return
	}
	email := in.email()
	if !strings.Contains(email, "@") {
		fail(w, http.StatusBadRequest, "invalidValue", "userName or emails must hold an e-mail address")
		return
	}
	orgID, err := h.org(ctx)
	if err != nil {
		fail(w, http.StatusInternalServerError, "", err.Error())
		return
	}
	if _, err := h.store.GetUserByEmail(ctx, orgID, email); err == nil {
		fail(w, http.StatusConflict, "uniqueness", "a user with this e-mail exists")
		return
	}
	u, err := h.store.CreateUser(ctx, orgID, email, in.fullName(), store.RoleViewer)
	if errors.Is(err, store.ErrExists) {
		fail(w, http.StatusConflict, "uniqueness", "a user with this e-mail exists")
		return
	} else if err != nil {
		fail(w, http.StatusInternalServerError, "", err.Error())
		return
	}
	if in.ExternalID != "" {
		_ = h.store.SetUserProfile(ctx, orgID, u.ID, u.Email, u.Name, in.ExternalID)
	}
	if in.Active != nil && !*in.Active {
		_ = h.store.SetUserDisabled(ctx, orgID, u.ID, true)
	}
	if err := h.grant(ctx, u.ID); err != nil {
		fail(w, http.StatusInternalServerError, "", err.Error())
		return
	}
	h.audit(ctx, orgID, "scim.user.create", u.Email)
	u, _ = h.store.GetUser(ctx, u.ID)
	reply(w, http.StatusCreated, h.userResource(ctx, u))
}

func (h *Handler) replaceUser(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u, ok := h.user(w, r)
	if !ok {
		return
	}
	var in userResource
	if !decode(w, r, &in) {
		return
	}
	email := in.email()
	if email == "" {
		email = u.Email
	}
	active := true
	if in.Active != nil {
		active = *in.Active
	}
	if !h.update(w, r, u, email, in.fullName(), in.ExternalID, active) {
		return
	}
	u, _ = h.store.GetUser(ctx, u.ID)
	reply(w, http.StatusOK, h.userResource(ctx, u))
}

// patchOp is one operation of a PATCH (RFC 7644 3.5.2).
type patchOp struct {
	Op    string          `json:"op"`
	Path  string          `json:"path"`
	Value json.RawMessage `json:"value"`
}

func (h *Handler) patchUser(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u, ok := h.user(w, r)
	if !ok {
		return
	}
	var in struct {
		Operations []patchOp `json:"Operations"`
	}
	if !decode(w, r, &in) {
		return
	}
	email, fullName, ext, active := u.Email, u.Name, u.ExternalID, !u.Disabled
	set := func(path string, v json.RawMessage) error {
		switch strings.ToLower(path) {
		case "active":
			b, err := boolOf(v)
			active = b
			return err
		case "username", `emails[type eq "work"].value`, "emails[primary eq true].value":
			return json.Unmarshal(v, &email)
		case "displayname", "name.formatted":
			return json.Unmarshal(v, &fullName)
		case "externalid":
			return json.Unmarshal(v, &ext)
		case "name.givenname", "name.familyname", "title", "preferredlanguage", "locale", "nickname":
			return nil // kept by the provider; Goliash has one name
		}
		return fmt.Errorf("unsupported path %q", path)
	}
	for _, op := range in.Operations {
		switch strings.ToLower(op.Op) {
		case "replace", "add":
			if op.Path == "" { // the value is an object of attributes
				var attrs map[string]json.RawMessage
				if err := json.Unmarshal(op.Value, &attrs); err != nil {
					fail(w, http.StatusBadRequest, "invalidValue", err.Error())
					return
				}
				for k, v := range attrs {
					if err := set(k, v); err != nil {
						fail(w, http.StatusBadRequest, "invalidPath", err.Error())
						return
					}
				}
				continue
			}
			if err := set(op.Path, op.Value); err != nil {
				fail(w, http.StatusBadRequest, "invalidPath", err.Error())
				return
			}
		case "remove":
			// Removing an attribute Goliash needs (the e-mail) is refused; others are kept by the provider.
			if strings.EqualFold(op.Path, "userName") {
				fail(w, http.StatusBadRequest, "mutability", "userName is required")
				return
			}
		default:
			fail(w, http.StatusBadRequest, "invalidSyntax", "unknown op "+op.Op)
			return
		}
	}
	if !h.update(w, r, u, email, fullName, ext, active) {
		return
	}
	u, _ = h.store.GetUser(ctx, u.ID)
	reply(w, http.StatusOK, h.userResource(ctx, u))
}

// update writes a person's attributes; deactivating ends their sessions. The last active owner is not deactivated.
func (h *Handler) update(w http.ResponseWriter, r *http.Request, u store.User, email, fullName, ext string, active bool) bool {
	ctx := r.Context()
	if !strings.EqualFold(email, u.Email) {
		if other, err := h.store.GetUserByEmail(ctx, u.OrgID, email); err == nil && other.ID != u.ID {
			fail(w, http.StatusConflict, "uniqueness", "a user with this e-mail exists")
			return false
		}
	}
	if err := h.store.SetUserProfile(ctx, u.OrgID, u.ID, email, fullName, ext); err != nil {
		fail(w, http.StatusInternalServerError, "", err.Error())
		return false
	}
	if active == u.Disabled { // a change
		if !active && h.lastOwner(ctx, u) {
			fail(w, http.StatusConflict, "mutability", "the last owner stays active")
			return false
		}
		if err := h.store.SetUserDisabled(ctx, u.OrgID, u.ID, !active); err != nil {
			fail(w, http.StatusInternalServerError, "", err.Error())
			return false
		}
		action := "scim.user.activate"
		if !active {
			action = "scim.user.deactivate"
		}
		h.audit(ctx, u.OrgID, action, email)
	}
	return true
}

func (h *Handler) deleteUser(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u, ok := h.user(w, r)
	if !ok {
		return
	}
	if h.lastOwner(ctx, u) {
		fail(w, http.StatusConflict, "mutability", "the last owner is not deleted")
		return
	}
	if err := h.store.DeleteUser(ctx, u.OrgID, u.ID); err != nil {
		fail(w, http.StatusInternalServerError, "", err.Error())
		return
	}
	h.audit(ctx, u.OrgID, "scim.user.delete", u.Email)
	w.WriteHeader(http.StatusNoContent)
}

// lastOwner reports whether u is the organization's only active owner.
func (h *Handler) lastOwner(ctx context.Context, u store.User) bool {
	if u.Role != store.RoleOwner {
		return false
	}
	users, err := h.store.ListUsers(ctx, u.OrgID)
	if err != nil {
		return true
	}
	for _, o := range users {
		if o.ID != u.ID && o.Role == store.RoleOwner && !o.Disabled {
			return false
		}
	}
	return true
}

// grant sets a person's roles from their SCIM groups as GOLIASH_GROUP_ROLES maps them: in no mapped group, no access
// at all. Without the mapping, a new person is a viewer of the first workspace. Owners are kept.
func (h *Handler) grant(ctx context.Context, userID string) error {
	u, err := h.store.GetUser(ctx, userID)
	if err != nil || u.Role == store.RoleOwner {
		return err
	}
	if len(h.roles) == 0 {
		ws, err := h.store.ListWorkspaces(ctx)
		if err != nil || len(ws) == 0 {
			return err
		}
		if acc, err := h.store.UserWorkspaces(ctx, u); err == nil && len(acc) == 0 {
			return h.store.SetMembership(ctx, u.ID, ws[0].ID, store.RoleViewer)
		}
		return nil
	}
	groups, err := h.store.UserSCIMGroups(ctx, u.ID)
	if err != nil {
		return err
	}
	var names []string
	for _, g := range groups {
		names = append(names, g.DisplayName, g.ExternalID)
	}
	grants, ok := h.roles.For(names)
	if !ok {
		grants = auth.Grants{OrgRole: store.RoleViewer, Workspaces: map[string]string{}} // signed in, sees nothing
	}
	return auth.ApplyGrants(ctx, h.store, u, grants)
}

// ---- groups ----

type groupResource struct {
	Schemas     []string `json:"schemas"`
	ID          string   `json:"id,omitempty"`
	ExternalID  string   `json:"externalId,omitempty"`
	DisplayName string   `json:"displayName"`
	Members     []multi  `json:"members,omitempty"`
	Meta        *meta    `json:"meta,omitempty"`
}

func (h *Handler) groupResource(ctx context.Context, g store.SCIMGroup) groupResource {
	out := groupResource{
		Schemas: []string{groupSchema}, ID: g.ID, ExternalID: g.ExternalID, DisplayName: g.DisplayName,
		Meta: &meta{ResourceType: "Group", Created: g.CreatedAt, Location: h.base + "/Groups/" + g.ID},
	}
	for _, id := range g.Members {
		m := multi{Value: id, Ref: h.base + "/Users/" + id}
		if u, err := h.store.GetUser(ctx, id); err == nil {
			m.Display = u.Email
		}
		out.Members = append(out.Members, m)
	}
	return out
}

func (h *Handler) listGroups(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	orgID, err := h.org(ctx)
	if err != nil {
		fail(w, http.StatusInternalServerError, "", err.Error())
		return
	}
	attr, value, err := filter(r.URL.Query().Get("filter"))
	if err != nil {
		fail(w, http.StatusBadRequest, "invalidFilter", err.Error())
		return
	}
	groups, err := h.store.ListSCIMGroups(ctx, orgID)
	if err != nil {
		fail(w, http.StatusInternalServerError, "", err.Error())
		return
	}
	excludeMembers := strings.Contains(strings.ToLower(r.URL.Query().Get("excludedAttributes")), "members")
	var found []any
	for _, g := range groups {
		switch strings.ToLower(attr) {
		case "":
		case "displayname":
			if !strings.EqualFold(g.DisplayName, value) {
				continue
			}
		case "externalid":
			if g.ExternalID != value {
				continue
			}
		case "id":
			if g.ID != value {
				continue
			}
		default:
			fail(w, http.StatusBadRequest, "invalidFilter", "filter on displayName, externalId or id")
			return
		}
		if !excludeMembers {
			if full, err := h.store.GetSCIMGroup(ctx, orgID, g.ID); err == nil {
				g = full
			}
		}
		found = append(found, h.groupResource(ctx, g))
	}
	list(w, r, found)
}

func (h *Handler) group(w http.ResponseWriter, r *http.Request) (store.SCIMGroup, bool) {
	orgID, err := h.org(r.Context())
	if err != nil {
		fail(w, http.StatusInternalServerError, "", err.Error())
		return store.SCIMGroup{}, false
	}
	g, err := h.store.GetSCIMGroup(r.Context(), orgID, r.PathValue("id"))
	if err != nil {
		fail(w, http.StatusNotFound, "", "no such group")
		return g, false
	}
	return g, true
}

func (h *Handler) getGroup(w http.ResponseWriter, r *http.Request) {
	if g, ok := h.group(w, r); ok {
		reply(w, http.StatusOK, h.groupResource(r.Context(), g))
	}
}

// members are the user IDs of members, those of the organization only.
func (h *Handler) members(ctx context.Context, orgID string, in []multi) []string {
	var out []string
	for _, m := range in {
		if u, err := h.store.GetUser(ctx, m.Value); err == nil && u.OrgID == orgID {
			out = append(out, u.ID)
		}
	}
	return out
}

func (h *Handler) createGroup(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var in groupResource
	if !decode(w, r, &in) {
		return
	}
	if strings.TrimSpace(in.DisplayName) == "" {
		fail(w, http.StatusBadRequest, "invalidValue", "displayName is required")
		return
	}
	orgID, err := h.org(ctx)
	if err != nil {
		fail(w, http.StatusInternalServerError, "", err.Error())
		return
	}
	if groups, err := h.store.ListSCIMGroups(ctx, orgID); err == nil {
		for _, g := range groups {
			if strings.EqualFold(g.DisplayName, in.DisplayName) {
				fail(w, http.StatusConflict, "uniqueness", "a group with this name exists")
				return
			}
		}
	}
	g, err := h.store.CreateSCIMGroup(ctx, store.SCIMGroup{OrgID: orgID, DisplayName: in.DisplayName, ExternalID: in.ExternalID, Members: h.members(ctx, orgID, in.Members)})
	if err != nil {
		fail(w, http.StatusInternalServerError, "", err.Error())
		return
	}
	h.regrant(ctx, g.Members)
	h.audit(ctx, orgID, "scim.group.create", g.DisplayName)
	reply(w, http.StatusCreated, h.groupResource(ctx, g))
}

func (h *Handler) replaceGroup(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	g, ok := h.group(w, r)
	if !ok {
		return
	}
	var in groupResource
	if !decode(w, r, &in) {
		return
	}
	if in.DisplayName == "" {
		in.DisplayName = g.DisplayName
	}
	if err := h.store.UpdateSCIMGroup(ctx, g.OrgID, g.ID, in.DisplayName, in.ExternalID); err != nil {
		fail(w, http.StatusInternalServerError, "", err.Error())
		return
	}
	next := h.members(ctx, g.OrgID, in.Members)
	if err := h.store.RemoveSCIMGroupMembers(ctx, g.ID, nil); err != nil {
		fail(w, http.StatusInternalServerError, "", err.Error())
		return
	}
	if err := h.store.AddSCIMGroupMembers(ctx, g.ID, next); err != nil {
		fail(w, http.StatusInternalServerError, "", err.Error())
		return
	}
	h.regrant(ctx, append(g.Members, next...))
	h.audit(ctx, g.OrgID, "scim.group.update", in.DisplayName)
	g, _ = h.store.GetSCIMGroup(ctx, g.OrgID, g.ID)
	reply(w, http.StatusOK, h.groupResource(ctx, g))
}

func (h *Handler) patchGroup(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	g, ok := h.group(w, r)
	if !ok {
		return
	}
	var in struct {
		Operations []patchOp `json:"Operations"`
	}
	if !decode(w, r, &in) {
		return
	}
	touched := append([]string{}, g.Members...)
	name, ext := g.DisplayName, g.ExternalID
	for _, op := range in.Operations {
		path := strings.ToLower(strings.TrimSpace(op.Path))
		switch {
		case path == "members" || (path == "" && strings.Contains(string(op.Value), `"members"`)):
			var ms []multi
			raw := op.Value
			if path == "" {
				var obj struct {
					Members []multi `json:"members"`
				}
				_ = json.Unmarshal(raw, &obj)
				ms = obj.Members
			} else if err := json.Unmarshal(raw, &ms); err != nil {
				fail(w, http.StatusBadRequest, "invalidValue", err.Error())
				return
			}
			ids := h.members(ctx, g.OrgID, ms)
			touched = append(touched, ids...)
			var err error
			switch strings.ToLower(op.Op) {
			case "add":
				err = h.store.AddSCIMGroupMembers(ctx, g.ID, ids)
			case "remove":
				if len(ms) == 0 {
					ids = nil // remove every member
				}
				err = h.store.RemoveSCIMGroupMembers(ctx, g.ID, ids)
			case "replace":
				if err = h.store.RemoveSCIMGroupMembers(ctx, g.ID, nil); err == nil {
					err = h.store.AddSCIMGroupMembers(ctx, g.ID, ids)
				}
			}
			if err != nil {
				fail(w, http.StatusInternalServerError, "", err.Error())
				return
			}
		case strings.HasPrefix(path, "members[value eq "):
			// remove members[value eq "id"]
			id := strings.Trim(strings.TrimSuffix(op.Path[len("members[value eq "):], "]"), `" `)
			if err := h.store.RemoveSCIMGroupMembers(ctx, g.ID, []string{id}); err != nil {
				fail(w, http.StatusInternalServerError, "", err.Error())
				return
			}
			touched = append(touched, id)
		case path == "displayname":
			_ = json.Unmarshal(op.Value, &name)
		case path == "externalid":
			_ = json.Unmarshal(op.Value, &ext)
		case path == "":
			var obj struct {
				DisplayName string `json:"displayName"`
				ExternalID  string `json:"externalId"`
			}
			_ = json.Unmarshal(op.Value, &obj)
			if obj.DisplayName != "" {
				name = obj.DisplayName
			}
			if obj.ExternalID != "" {
				ext = obj.ExternalID
			}
		default:
			fail(w, http.StatusBadRequest, "invalidPath", "unsupported path "+op.Path)
			return
		}
	}
	if name != g.DisplayName || ext != g.ExternalID {
		if err := h.store.UpdateSCIMGroup(ctx, g.OrgID, g.ID, name, ext); err != nil {
			fail(w, http.StatusInternalServerError, "", err.Error())
			return
		}
	}
	h.regrant(ctx, touched)
	h.audit(ctx, g.OrgID, "scim.group.update", name)
	g, _ = h.store.GetSCIMGroup(ctx, g.OrgID, g.ID)
	reply(w, http.StatusOK, h.groupResource(ctx, g))
}

func (h *Handler) deleteGroup(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	g, ok := h.group(w, r)
	if !ok {
		return
	}
	if err := h.store.DeleteSCIMGroup(ctx, g.OrgID, g.ID); err != nil {
		fail(w, http.StatusInternalServerError, "", err.Error())
		return
	}
	h.regrant(ctx, g.Members)
	h.audit(ctx, g.OrgID, "scim.group.delete", g.DisplayName)
	w.WriteHeader(http.StatusNoContent)
}

// regrant sets the roles of people whose groups changed.
func (h *Handler) regrant(ctx context.Context, userIDs []string) {
	seen := map[string]bool{}
	for _, id := range userIDs {
		if seen[id] {
			continue
		}
		seen[id] = true
		if err := h.grant(ctx, id); err != nil {
			h.log.WarnContext(ctx, "scim: roles not set", "user", id, "err", err)
		}
	}
}

// ---- discovery ----

func (h *Handler) serviceProviderConfig(w http.ResponseWriter, _ *http.Request) {
	reply(w, http.StatusOK, map[string]any{
		"schemas":          []string{"urn:ietf:params:scim:schemas:core:2.0:ServiceProviderConfig"},
		"documentationUri": "https://goliash.dev/reference/configuration/#scim-provisioning",
		"patch":            map[string]bool{"supported": true},
		"bulk":             map[string]any{"supported": false, "maxOperations": 0, "maxPayloadSize": 0},
		"filter":           map[string]any{"supported": true, "maxResults": 1000},
		"changePassword":   map[string]bool{"supported": false},
		"sort":             map[string]bool{"supported": false},
		"etag":             map[string]bool{"supported": false},
		"authenticationSchemes": []map[string]any{{
			"type": "oauthbearertoken", "name": "Bearer token", "description": "GOLIASH_SCIM_TOKEN", "primary": true,
		}},
	})
}

func (h *Handler) resourceTypes(w http.ResponseWriter, r *http.Request) {
	list(w, r, []any{
		map[string]any{"schemas": []string{"urn:ietf:params:scim:schemas:core:2.0:ResourceType"}, "id": "User", "name": "User", "endpoint": "/Users", "schema": userSchema},
		map[string]any{"schemas": []string{"urn:ietf:params:scim:schemas:core:2.0:ResourceType"}, "id": "Group", "name": "Group", "endpoint": "/Groups", "schema": groupSchema},
	})
}

func (h *Handler) schemas(w http.ResponseWriter, r *http.Request) {
	attr := func(name, typ string, required bool) map[string]any {
		return map[string]any{"name": name, "type": typ, "multiValued": false, "required": required, "mutability": "readWrite", "returned": "default"}
	}
	list(w, r, []any{
		map[string]any{"id": userSchema, "name": "User", "attributes": []any{attr("userName", "string", true), attr("displayName", "string", false), attr("active", "boolean", false), attr("externalId", "string", false)}},
		map[string]any{"id": groupSchema, "name": "Group", "attributes": []any{attr("displayName", "string", true), attr("externalId", "string", false)}},
	})
}

// ---- helpers ----

// filter reads the simple filters providers send: `userName eq "a@b.c"`. Empty matches everything.
func filter(f string) (attr, value string, err error) {
	f = strings.TrimSpace(f)
	if f == "" {
		return "", "", nil
	}
	parts := strings.SplitN(f, " ", 3)
	if len(parts) != 3 || !strings.EqualFold(parts[1], "eq") {
		return "", "", fmt.Errorf("only attribute eq \"value\" filters are supported, not %q", f)
	}
	v, err := strconv.Unquote(parts[2])
	if err != nil {
		v = strings.Trim(parts[2], `"`)
	}
	return parts[0], v, nil
}

// boolOf reads a boolean, also as the string Entra ID sends ("False").
func boolOf(v json.RawMessage) (bool, error) {
	var b bool
	if err := json.Unmarshal(v, &b); err == nil {
		return b, nil
	}
	var s string
	if err := json.Unmarshal(v, &s); err != nil {
		return false, fmt.Errorf("active: %w", err)
	}
	return strconv.ParseBool(strings.ToLower(s))
}

func list(w http.ResponseWriter, r *http.Request, items []any) {
	start, _ := strconv.Atoi(r.URL.Query().Get("startIndex"))
	if start < 1 {
		start = 1
	}
	count, err := strconv.Atoi(r.URL.Query().Get("count"))
	if err != nil || count < 0 {
		count = len(items)
	}
	page := []any{}
	if start-1 < len(items) {
		page = items[start-1 : min(len(items), start-1+count)]
	}
	reply(w, http.StatusOK, map[string]any{
		"schemas": []string{listSchema}, "totalResults": len(items), "startIndex": start, "itemsPerPage": len(page), "Resources": page,
	})
}

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(v); err != nil {
		fail(w, http.StatusBadRequest, "invalidSyntax", err.Error())
		return false
	}
	return true
}

func reply(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", contentType)
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func fail(w http.ResponseWriter, status int, scimType, detail string) {
	body := map[string]any{"schemas": []string{errorSchema}, "status": strconv.Itoa(status), "detail": detail}
	if scimType != "" {
		body["scimType"] = scimType
	}
	reply(w, status, body)
}

func (h *Handler) audit(ctx context.Context, orgID, action, subject string) {
	_ = h.store.Audit(ctx, store.AuditEntry{OrgID: orgID, Actor: "scim", Action: action, Details: map[string]string{"subject": subject}})
}
