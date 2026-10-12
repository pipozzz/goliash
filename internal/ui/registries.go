// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package ui

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/pipozzz/goliash/internal/auth"
	"github.com/pipozzz/goliash/internal/registry"
	"github.com/pipozzz/goliash/internal/registry/ecr"
	"github.com/pipozzz/goliash/internal/store"
	"github.com/pipozzz/goliash/internal/versions"
)

// RegistryView is a registry the workspace gave the server credentials for.
type RegistryView struct {
	ID, Host, Username, CreatedBy string
	CreatedAt, UsedAt             time.Time
	Error                         string
	Services                      int // services whose upstream is on this registry
}

// UnreadRegistry is a registry the workspace's services use that only the agents read.
type UnreadRegistry struct {
	Host     string
	Services int
	ECR      bool // Amazon ECR: read by an agent with its IAM role, never with stored credentials
}

// isECR reports whether host is an Amazon ECR registry.
func isECR(host string) bool {
	_, _, _, ok := ecr.Parse(host + "/x")
	return ok
}

// registriesView lists the workspace's registry credentials, and the private registries its services
// use that the server cannot read yet.
func (s *Server) registriesView(ctx context.Context, sc store.Scope) ([]RegistryView, []UnreadRegistry, error) {
	creds, err := s.store.ListRegistryCredentials(ctx, sc)
	if err != nil {
		return nil, nil, err
	}
	o, err := versions.LoadOverview(ctx, s.store, sc)
	if err != nil {
		return nil, nil, err
	}
	uses, unread := map[string]int{}, map[string]int{}
	for id, ref := range o.Refs {
		if ref.Repo == "" || versions.PackageRepo(ref.Repo) {
			continue
		}
		host := versions.RegistryHost(ref.Repo)
		uses[host]++
		if versions.CheckedByAgent(o.Services[id], ref.Repo, o.Registries) {
			unread[host]++
		}
	}
	var out []RegistryView
	for _, c := range creds {
		out = append(out, RegistryView{
			ID: c.ID, Host: c.Host, Username: c.Username, CreatedBy: c.CreatedBy, CreatedAt: c.CreatedAt, UsedAt: c.UsedAt,
			Error: c.Error, Services: uses[c.Host],
		})
	}
	var missing []UnreadRegistry
	for host, n := range unread {
		missing = append(missing, UnreadRegistry{Host: host, Services: n, ECR: isECR(host)})
	}
	sort.Slice(missing, func(i, j int) bool { return missing[i].Host < missing[j].Host })
	return out, missing, nil
}

// validRegistryHost accepts a registry host with an optional port, as images name it. The server's own
// machine and cloud metadata endpoints are not registries: credentials are never sent there.
func validRegistryHost(h string) bool {
	if h == "" || len(h) > 253 || !strings.Contains(h, ".") {
		return false
	}
	for _, r := range h {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '.' && r != '-' && r != ':' {
			return false
		}
	}
	name := h
	if host, _, err := net.SplitHostPort(h); err == nil {
		name = host
	}
	if ip := net.ParseIP(name); ip != nil && (ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsUnspecified()) {
		return false
	}
	return name != "metadata.google.internal" && !strings.HasSuffix(name, ".localhost")
}

// saveRegistry stores credentials for a registry and tries them on a repository the workspace uses.
func (s *Server) saveRegistry(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	ctx := r.Context()
	host := registry.NormalizeHost(r.FormValue("host"))
	user := strings.TrimSpace(r.FormValue("username"))
	secret := strings.TrimSpace(r.FormValue("secret"))
	switch {
	case isECR(host):
		return back(w, r, "/settings#registries", "error", "Amazon ECR tokens expire after 12 hours: an agent reads ECR with its IAM role (ecr:ListImages, or AmazonEC2ContainerRegistryReadOnly).")
	case !validRegistryHost(host):
		return back(w, r, "/settings#registries", "error", "Enter the registry host as images name it, e.g. registry.example.com or ghcr.io.")
	case secret == "":
		return back(w, r, "/settings#registries", "error", "Enter the password or access token.")
	case len(user) > 200 || len(secret) > 8192:
		return back(w, r, "/settings#registries", "error", "That username or token is too long.")
	}
	if user == "" {
		user = "token" // registries that take a bare token accept any username
	}
	if _, err := s.store.SetRegistryCredential(ctx, store.RegistryCredential{
		Scope: p.Scope, Host: host, Username: user, Secret: secret, CreatedBy: p.Name(),
	}); errors.Is(err, store.ErrSecretsUnencrypted) {
		return back(w, r, "/settings#registries", "error", "Set GOLIASH_SECRET_KEY on the server first: registry credentials are only stored encrypted.")
	} else if err != nil {
		return err
	}
	s.audit(ctx, p, "registry.credentials", "host", host, "username", user)
	if s.checker == nil {
		return back(w, r, "/settings#registries", "notice", "Saved the credentials for "+host+".")
	}
	try, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	repo, tags, err := s.checker.TryRegistry(try, p.Scope, host)
	switch {
	case repo == "" && err == nil:
		return back(w, r, "/settings#registries", "notice", "Saved. No service uses "+host+" yet; Goliash reads it with these credentials once one does.")
	case errors.Is(err, registry.ErrUnauthorized):
		return back(w, r, "/settings#registries", "error", "Saved, but "+host+" refused them for "+repo+". Check the username and that the token may read it (pull or read:packages).")
	case err != nil:
		return back(w, r, "/settings#registries", "error", "Saved, but reading "+repo+" failed: "+err.Error())
	}
	// Releases, digests, signatures and SBOMs of the registry's images, without waiting for the next round.
	go func(sc store.Scope) {
		bg, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Minute)
		defer cancel()
		s.checker.RefreshRegistry(bg, sc)
		s.hub.Publish(sc.WorkspaceID)
	}(p.Scope)
	return back(w, r, "/settings#registries", "notice", fmt.Sprintf("%s accepted them: %d tags in %s. The server reads its images itself now; the matrix updates in a minute.", host, tags, repo))
}

// deleteRegistry removes a registry's credentials; its repositories go back to the agents.
func (s *Server) deleteRegistry(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	host, err := s.store.DeleteRegistryCredential(r.Context(), p.Scope, r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		return back(w, r, "/settings#registries", "error", "Those credentials are gone already.")
	} else if err != nil {
		return err
	}
	s.audit(r.Context(), p, "registry.credentials.delete", "host", host)
	return back(w, r, "/settings#registries", "notice", "Removed the credentials for "+host+". Agents with their own credentials still read it.")
}
