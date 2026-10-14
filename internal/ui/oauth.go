// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package ui

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/pipozzz/goliash/internal/auth"
	"github.com/pipozzz/goliash/internal/store"
	"github.com/pipozzz/goliash/internal/tokens"
)

// OAuth 2.1 for MCP clients, as the MCP authorization specification asks: Goliash is both the protected
// resource (/mcp) and its authorization server. An app (Claude, Cursor, …) finds the server from the 401 on
// /mcp, identifies itself with a Client ID Metadata Document (or registers itself, the older way), sends the
// person to /oauth/authorize, where they sign in as usual and allow it, and gets tokens with PKCE. The app then
// reads Goliash as that person, read only, in the workspace they chose, until they revoke it on their account
// page or stop being a member.

const (
	oauthScope       = "goliash:read"
	oauthCodeTTL     = 10 * time.Minute
	oauthAccessTTL   = time.Hour
	oauthRefreshTTL  = 30 * 24 * time.Hour
	oauthMaxClients  = 1000      // self-registered clients waiting for someone to allow them
	oauthMetadataTTL = time.Hour // how long a fetched client metadata document is trusted
	oauthMetadataMax = 64 << 10  // bytes of a client metadata document
	oauthClientName  = 100       // characters of an app's name shown on the consent page
	oauthVerifierMin = 43        // PKCE code verifier length (RFC 7636)
	oauthVerifierMax = 128       //
	oauthStateMax    = 2000      // characters of the client's state echoed back
	oauthRedirectMax = 2000      // characters of a redirect URI
	oauthMaxRedirect = 10        // redirect URIs per client
	oauthRegisterMax = 16 << 10  // bytes of a registration request
	oauthGrantTypes  = "authorization_code refresh_token"
	oauthTokenType   = "Bearer"
	oauthNoStore     = "no-store"
	oauthJSON        = "application/json"
	oauthChallengeS  = "S256"
	oauthCodeType    = "code"
	oauthAuthNone    = "none"
	oauthResourceMCP = "/mcp"
)

// oauthIssuer is this server's address: the issuer, and the base of every OAuth URL.
func (s *Server) oauthIssuer(r *http.Request) string {
	if s.publicURL != "" {
		return s.publicURL
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

// oauthResource is the MCP server's canonical URI, the audience of every access token.
func (s *Server) oauthResource(r *http.Request) string { return s.oauthIssuer(r) + oauthResourceMCP }

// OAuthResourceMetadataURL is where an app learns how to get a token for /mcp (RFC 9728).
func (s *Server) OAuthResourceMetadataURL(r *http.Request) string {
	return s.oauthIssuer(r) + "/.well-known/oauth-protected-resource" + oauthResourceMCP
}

func (s *Server) oauthRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /.well-known/oauth-protected-resource", s.oauthResourceMetadata)
	mux.HandleFunc("GET /.well-known/oauth-protected-resource/mcp", s.oauthResourceMetadata)
	mux.HandleFunc("GET /.well-known/oauth-authorization-server", s.oauthServerMetadata)
	mux.HandleFunc("POST /oauth/register", s.oauthRegister)
	mux.Handle("GET /oauth/authorize", s.page(store.RoleViewer, s.oauthAuthorize))
	mux.Handle("POST /oauth/authorize", s.page(store.RoleViewer, s.oauthDecide))
	mux.HandleFunc("POST /oauth/token", s.oauthToken)
	mux.HandleFunc("POST /oauth/revoke", s.oauthRevoke)
	mux.Handle("POST /account/apps/{id}/revoke", s.page(store.RoleViewer, s.oauthRevokeApp))
}

func (s *Server) oauthResourceMetadata(w http.ResponseWriter, r *http.Request) {
	oauthWrite(w, http.StatusOK, map[string]any{
		"resource":                 s.oauthResource(r),
		"authorization_servers":    []string{s.oauthIssuer(r)},
		"scopes_supported":         []string{oauthScope},
		"bearer_methods_supported": []string{"header"},
		"resource_name":            "Goliash",
		"resource_documentation":   "https://goliash.dev/reference/mcp/",
	})
}

func (s *Server) oauthServerMetadata(w http.ResponseWriter, r *http.Request) {
	iss := s.oauthIssuer(r)
	oauthWrite(w, http.StatusOK, map[string]any{
		"issuer":                                         iss,
		"authorization_endpoint":                         iss + "/oauth/authorize",
		"token_endpoint":                                 iss + "/oauth/token",
		"registration_endpoint":                          iss + "/oauth/register",
		"revocation_endpoint":                            iss + "/oauth/revoke",
		"response_types_supported":                       []string{oauthCodeType},
		"grant_types_supported":                          strings.Fields(oauthGrantTypes),
		"code_challenge_methods_supported":               []string{oauthChallengeS},
		"token_endpoint_auth_methods_supported":          []string{oauthAuthNone},
		"revocation_endpoint_auth_methods_supported":     []string{oauthAuthNone},
		"scopes_supported":                               []string{oauthScope},
		"client_id_metadata_document_supported":          true,
		"authorization_response_iss_parameter_supported": true,
		"service_documentation":                          "https://goliash.dev/reference/mcp/",
	})
}

func oauthWrite(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", oauthJSON)
	w.Header().Set("Cache-Control", oauthNoStore)
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func oauthError(w http.ResponseWriter, status int, code, description string) {
	oauthWrite(w, status, map[string]string{"error": code, "error_description": description})
}

// validRedirect accepts https addresses and the loopback addresses native apps listen on (RFC 8252), without
// fragments or credentials.
func validRedirect(raw string) bool {
	if len(raw) > oauthRedirectMax {
		return false
	}
	u, err := url.Parse(raw)
	if err != nil || u.Fragment != "" || u.User != nil || u.Host == "" {
		return false
	}
	switch u.Scheme {
	case "https":
		return true
	case "http":
		return loopbackHost(u.Hostname())
	}
	return false
}

func loopbackHost(h string) bool {
	if h == "localhost" {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

// redirectAllowed matches a redirect URI against a client's: exactly, except that a loopback one may use any
// port, as native apps pick a free one each time (RFC 8252 section 7.3).
func redirectAllowed(allowed []string, got string) bool {
	if slices.Contains(allowed, got) {
		return true
	}
	g, err := url.Parse(got)
	if err != nil || g.Scheme != "http" || !loopbackHost(g.Hostname()) {
		return false
	}
	for _, a := range allowed {
		u, err := url.Parse(a)
		if err == nil && u.Scheme == "http" && u.Hostname() == g.Hostname() && u.Path == g.Path && u.RawQuery == g.RawQuery {
			return true
		}
	}
	return false
}

// oauthRegistration is a client's metadata, sent to /oauth/register or served as its Client ID Metadata Document.
type oauthRegistration struct {
	ClientID                string   `json:"client_id,omitempty"`
	ClientName              string   `json:"client_name"`
	RedirectURIs            []string `json:"redirect_uris"`
	GrantTypes              []string `json:"grant_types,omitempty"`
	ResponseTypes           []string `json:"response_types,omitempty"`
	TokenEndpointAuthMethod string   `json:"token_endpoint_auth_method,omitempty"`
}

// check validates what Goliash relies on: where codes may go, and that the client needs no secret.
func (m oauthRegistration) check() error {
	if len(m.RedirectURIs) == 0 || len(m.RedirectURIs) > oauthMaxRedirect {
		return errors.New("redirect_uris must list one to ten addresses")
	}
	for _, u := range m.RedirectURIs {
		if !validRedirect(u) {
			return fmt.Errorf("redirect URI %q must be https, or http on a loopback address", u)
		}
	}
	if m.TokenEndpointAuthMethod != "" && m.TokenEndpointAuthMethod != oauthAuthNone {
		return errors.New("only public clients (token_endpoint_auth_method none) with PKCE are supported")
	}
	for _, g := range m.GrantTypes {
		if !strings.Contains(oauthGrantTypes, g) {
			return fmt.Errorf("grant type %q is not supported", g)
		}
	}
	for _, t := range m.ResponseTypes {
		if t != oauthCodeType {
			return fmt.Errorf("response type %q is not supported", t)
		}
	}
	return nil
}

func oauthAppName(name string) string {
	name = strings.Join(strings.Fields(name), " ")
	if r := []rune(name); len(r) > oauthClientName {
		name = string(r[:oauthClientName]) + "…"
	}
	if name == "" {
		return "An unnamed app"
	}
	return name
}

// oauthRegister is Dynamic Client Registration (RFC 7591), for apps that predate Client ID Metadata Documents.
func (s *Server) oauthRegister(w http.ResponseWriter, r *http.Request) {
	var m oauthRegistration
	if err := json.NewDecoder(io.LimitReader(r.Body, oauthRegisterMax)).Decode(&m); err != nil {
		oauthError(w, http.StatusBadRequest, "invalid_client_metadata", "send the client's metadata as JSON")
		return
	}
	if err := m.check(); err != nil {
		code := "invalid_client_metadata"
		if strings.Contains(err.Error(), "redirect") {
			code = "invalid_redirect_uri"
		}
		oauthError(w, http.StatusBadRequest, code, err.Error())
		return
	}
	if n, err := s.store.CountOAuthClients(r.Context()); err != nil || n >= oauthMaxClients {
		oauthError(w, http.StatusServiceUnavailable, "temporarily_unavailable", "too many apps registered; try again later")
		return
	}
	id, _ := tokens.New(tokens.OAuthClient)
	c := store.OAuthClient{ID: id, Name: oauthAppName(m.ClientName), RedirectURIs: m.RedirectURIs}
	if err := s.store.SaveOAuthClient(r.Context(), c); err != nil {
		s.log.ErrorContext(r.Context(), "oauth client registration failed", "err", err)
		oauthError(w, http.StatusInternalServerError, "server_error", "registration failed")
		return
	}
	oauthWrite(w, http.StatusCreated, map[string]any{
		"client_id": id, "client_id_issued_at": time.Now().Unix(), "client_name": c.Name,
		"redirect_uris": c.RedirectURIs, "grant_types": strings.Fields(oauthGrantTypes),
		"response_types": []string{oauthCodeType}, "token_endpoint_auth_method": oauthAuthNone,
	})
}

// metadataClientID reports whether a client_id is a Client ID Metadata Document's URL: https, with a path.
func metadataClientID(id string) bool {
	u, err := url.Parse(id)
	return err == nil && u.Scheme == "https" && u.Host != "" && u.Path != "" && u.Path != "/" && u.Fragment == "" &&
		u.User == nil && !strings.Contains(u.Path, "/./") && !strings.Contains(u.Path, "/../")
}

// oauthClient finds the app behind a client_id: fetched from its metadata document (cached for an hour), or
// registered earlier.
func (s *Server) oauthClient(r *http.Request, id string) (store.OAuthClient, error) {
	if !metadataClientID(id) {
		c, err := s.store.GetOAuthClient(r.Context(), id)
		if err != nil {
			return c, errors.New("this app is not known here; it has to register again")
		}
		return c, nil
	}
	if c, err := s.store.GetOAuthClient(r.Context(), id); err == nil && time.Since(c.FetchedAt) < oauthMetadataTTL {
		return c, nil
	}
	// Fetching a URL a stranger names is the point of client metadata documents: the client reaches public
	// addresses only (publicOnlyClient), with no redirects, a timeout and a size limit.
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, id, nil) //nolint:gosec // see above
	if err != nil {
		return store.OAuthClient{}, err
	}
	req.Header.Set("Accept", oauthJSON)
	resp, err := s.oauthHTTP.Do(req) //nolint:gosec // public addresses only; see above
	if err != nil {
		return store.OAuthClient{}, errors.New("the app's metadata document could not be fetched")
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return store.OAuthClient{}, fmt.Errorf("the app's metadata document answered %d", resp.StatusCode)
	}
	var m oauthRegistration
	if err := json.NewDecoder(io.LimitReader(resp.Body, oauthMetadataMax)).Decode(&m); err != nil {
		return store.OAuthClient{}, errors.New("the app's metadata document is not JSON")
	}
	if m.ClientID != id {
		return store.OAuthClient{}, errors.New("the app's metadata document names another client_id")
	}
	if err := m.check(); err != nil {
		return store.OAuthClient{}, err
	}
	c := store.OAuthClient{ID: id, Name: oauthAppName(m.ClientName), RedirectURIs: m.RedirectURIs, MetadataURL: id}
	if err := s.store.SaveOAuthClient(r.Context(), c); err != nil {
		return store.OAuthClient{}, err
	}
	return c, nil
}

// oauthRequest is an authorization request, checked.
type oauthRequest struct {
	Client      store.OAuthClient
	RedirectURI string
	State       string
	Challenge   string
	Resource    string
	Verified    string // where the app's identity comes from, for the consent page
}

// parseAuthorize checks an authorization request. Until the client and its redirect URI check out, errors are
// shown here, never sent to the redirect URI (RFC 6749 section 4.1.2.1); after, back is set and they go there.
func (s *Server) parseAuthorize(r *http.Request, q url.Values) (req oauthRequest, back bool, code, msg string) {
	c, err := s.oauthClient(r, q.Get("client_id"))
	if err != nil {
		return req, false, "invalid_client", err.Error()
	}
	req.Client = c
	req.RedirectURI = q.Get("redirect_uri")
	if req.RedirectURI == "" || !redirectAllowed(c.RedirectURIs, req.RedirectURI) {
		return req, false, "invalid_request", "The app asked to send you back to an address it did not register."
	}
	if req.State = q.Get("state"); len(req.State) > oauthStateMax {
		return req, false, "invalid_request", "The app's state is too long."
	}
	if q.Get("response_type") != oauthCodeType {
		return req, true, "unsupported_response_type", "only the authorization code flow is supported"
	}
	req.Challenge = q.Get("code_challenge")
	if q.Get("code_challenge_method") != oauthChallengeS || len(req.Challenge) != 43 {
		return req, true, "invalid_request", "PKCE with S256 is required"
	}
	req.Resource = s.oauthResource(r)
	if res := q.Get("resource"); res != "" && !sameResource(res, req.Resource, s.oauthIssuer(r)) {
		return req, true, "invalid_target", "tokens here are only for " + req.Resource
	}
	if c.MetadataURL != "" {
		u, _ := url.Parse(c.MetadataURL)
		req.Verified = "described by " + u.Host
	} else {
		u, _ := url.Parse(req.RedirectURI)
		req.Verified = "registered itself; it will send you back to " + u.Host
	}
	return req, false, "", ""
}

// sameResource accepts the MCP server's URI, or the server's, in any letter case of scheme and host and with or
// without a trailing slash.
func sameResource(got, mcp, issuer string) bool {
	norm := func(s string) string {
		u, err := url.Parse(strings.TrimSuffix(s, "/"))
		if err != nil {
			return s
		}
		u.Scheme, u.Host = strings.ToLower(u.Scheme), strings.ToLower(u.Host)
		return u.String()
	}
	g := norm(got)
	return g == norm(mcp) || g == norm(issuer)
}

// oauthBack sends the person back to the app with a result, and the issuer so it can tell servers apart
// (RFC 9207).
func (s *Server) oauthBack(w http.ResponseWriter, r *http.Request, req oauthRequest, params url.Values) {
	u, _ := url.Parse(req.RedirectURI)
	q := u.Query()
	for k, v := range params {
		q[k] = v
	}
	if req.State != "" {
		q.Set("state", req.State)
	}
	q.Set("iss", s.oauthIssuer(r))
	u.RawQuery = q.Encode()
	http.Redirect(w, r, u.String(), http.StatusFound)
}

// oauthAuthorize asks the signed-in person whether to let the app read Goliash as them.
func (s *Server) oauthAuthorize(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	req, back, code, msg := s.parseAuthorize(r, r.URL.Query())
	if code != "" {
		if back {
			s.oauthBack(w, r, req, url.Values{"error": {code}, "error_description": {msg}})
			return nil
		}
		s.problem(w, r, http.StatusBadRequest, "This app cannot connect", msg, true)
		return nil
	}
	w.Header().Set("Cache-Control", oauthNoStore)
	// The answer redirects to the app, and browsers hold a form's redirects to form-action too: allow the one
	// address this app registered and is checked against, nothing else.
	if u, err := url.Parse(req.RedirectURI); err == nil {
		w.Header().Set("Content-Security-Policy", strings.Replace(contentSecurityPolicy,
			"form-action 'self'", "form-action 'self' "+u.Scheme+"://"+u.Host, 1))
	}
	return render(w, r, OAuthConsentPage(OAuthConsentView{
		Base: s.base(r.Context(), p, "", "Allow "+req.Client.Name), App: req.Client.Name, Verified: req.Verified,
		Params: r.URL.Query(), Workspaces: p.Workspaces, Current: p.Scope.WorkspaceID,
	}))
}

// oauthDecide records the person's answer: a code for the app, or a refusal.
func (s *Server) oauthDecide(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	if err := r.ParseForm(); err != nil {
		return err
	}
	req, back, code, msg := s.parseAuthorize(r, r.PostForm)
	if code != "" {
		if back {
			s.oauthBack(w, r, req, url.Values{"error": {code}, "error_description": {msg}})
			return nil
		}
		s.problem(w, r, http.StatusBadRequest, "This app cannot connect", msg, true)
		return nil
	}
	if r.PostForm.Get("decision") != "allow" {
		s.oauthBack(w, r, req, url.Values{"error": {"access_denied"}, "error_description": {"the person did not allow it"}})
		return nil
	}
	ws := r.PostForm.Get("workspace")
	if !slices.ContainsFunc(p.Workspaces, func(a store.Access) bool { return a.Workspace.ID == ws && a.Role != "" }) {
		s.problem(w, r, http.StatusForbidden, "Not your workspace", "Pick a workspace you can open.", true)
		return nil
	}
	raw, hash := tokens.New(tokens.OAuthCode)
	if err := s.store.CreateOAuthCode(r.Context(), store.OAuthCode{
		Hash: hash, ClientID: req.Client.ID, UserID: p.User.ID, WorkspaceID: ws, RedirectURI: req.RedirectURI,
		Challenge: req.Challenge, Scope: oauthScope, Resource: req.Resource, ExpiresAt: time.Now().Add(oauthCodeTTL),
	}); err != nil {
		return err
	}
	s.audit(r.Context(), p, "oauth.allow", "app", req.Client.Name, "client_id", req.Client.ID, "workspace", ws)
	s.oauthBack(w, r, req, url.Values{"code": {raw}})
	return nil
}

// oauthToken exchanges a code, or a refresh token, for tokens.
func (s *Server) oauthToken(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		oauthError(w, http.StatusBadRequest, "invalid_request", "send the request as a form")
		return
	}
	f := r.PostForm
	clientID := f.Get("client_id")
	if clientID == "" {
		oauthError(w, http.StatusUnauthorized, "invalid_client", "client_id is required")
		return
	}
	access, accessHash := tokens.New(tokens.OAuthAccess)
	refresh, refreshHash := tokens.New(tokens.OAuthRefresh)
	now := time.Now()
	next := store.OAuthGrant{
		AccessHash: accessHash, AccessExpiresAt: now.Add(oauthAccessTTL),
		RefreshHash: refreshHash, RefreshExpiresAt: now.Add(oauthRefreshTTL),
	}
	switch f.Get("grant_type") {
	case "authorization_code":
		c, err := s.store.ConsumeOAuthCode(r.Context(), tokens.Hash(f.Get("code")))
		if err != nil || c.ClientID != clientID || c.RedirectURI != f.Get("redirect_uri") {
			oauthError(w, http.StatusBadRequest, "invalid_grant", "the code is wrong, used or expired")
			return
		}
		if !pkceMatches(f.Get("code_verifier"), c.Challenge) {
			oauthError(w, http.StatusBadRequest, "invalid_grant", "the code verifier does not match")
			return
		}
		if res := f.Get("resource"); res != "" && !sameResource(res, c.Resource, s.oauthIssuer(r)) {
			oauthError(w, http.StatusBadRequest, "invalid_target", "tokens here are only for "+c.Resource)
			return
		}
		client, err := s.store.GetOAuthClient(r.Context(), clientID)
		if err != nil {
			oauthError(w, http.StatusUnauthorized, "invalid_client", "the app is no longer known")
			return
		}
		next.UserID, next.WorkspaceID, next.ClientID, next.ClientName = c.UserID, c.WorkspaceID, clientID, client.Name
		next.Scope, next.Resource = c.Scope, c.Resource
		if _, err := s.store.CreateOAuthGrant(r.Context(), next); err != nil {
			s.log.ErrorContext(r.Context(), "oauth grant failed", "err", err)
			oauthError(w, http.StatusInternalServerError, "server_error", "could not issue tokens")
			return
		}
	case "refresh_token":
		g, err := s.store.RotateOAuthGrant(r.Context(), tokens.Hash(f.Get("refresh_token")), clientID, next)
		if err != nil {
			oauthError(w, http.StatusBadRequest, "invalid_grant", "the refresh token is wrong, used, revoked or expired")
			return
		}
		next.Scope = g.Scope
	default:
		oauthError(w, http.StatusBadRequest, "unsupported_grant_type", "use authorization_code or refresh_token")
		return
	}
	w.Header().Set("Pragma", "no-cache")
	oauthWrite(w, http.StatusOK, map[string]any{
		"access_token": access, "token_type": oauthTokenType, "expires_in": int(oauthAccessTTL.Seconds()),
		"refresh_token": refresh, "scope": next.Scope,
	})
}

// pkceMatches checks a code verifier against its S256 challenge (RFC 7636), in constant time.
func pkceMatches(verifier, challenge string) bool {
	if len(verifier) < oauthVerifierMin || len(verifier) > oauthVerifierMax {
		return false
	}
	sum := sha256.Sum256([]byte(verifier))
	got := base64.RawURLEncoding.EncodeToString(sum[:])
	return subtle.ConstantTimeCompare([]byte(got), []byte(challenge)) == 1
}

// oauthRevoke ends a grant from the app's side (RFC 7009): it always answers 200.
func (s *Server) oauthRevoke(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		oauthError(w, http.StatusBadRequest, "invalid_request", "send the request as a form")
		return
	}
	if t := r.PostForm.Get("token"); t != "" {
		if err := s.store.RevokeOAuthToken(r.Context(), tokens.Hash(t), r.PostForm.Get("client_id")); err != nil {
			s.log.ErrorContext(r.Context(), "oauth revoke failed", "err", err)
		}
	}
	w.Header().Set("Cache-Control", oauthNoStore)
	w.WriteHeader(http.StatusOK)
}

// oauthRevokeApp ends one of the person's grants from their account page.
func (s *Server) oauthRevokeApp(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	if err := s.store.RevokeOAuthGrant(r.Context(), p.User.ID, r.PathValue("id")); errors.Is(err, store.ErrNotFound) {
		return back(w, r, "/account", "error", "That app no longer has access.")
	} else if err != nil {
		return err
	}
	s.audit(r.Context(), p, "oauth.revoke", "grant", r.PathValue("id"))
	return back(w, r, "/account", "notice", "The app's access is revoked.")
}
