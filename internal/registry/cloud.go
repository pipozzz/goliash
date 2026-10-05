// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: Apache-2.0

package registry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

// CloudAuth finds registry credentials from the cloud identity the agent runs with,
// without any configuration:
//
//   - Google Artifact Registry (*-docker.pkg.dev) and Container Registry (gcr.io):
//     an access token from the GCE metadata server, which GKE Workload Identity and
//     Compute Engine service accounts provide.
//   - Azure Container Registry (*.azurecr.io): AKS Workload Identity's federated token
//     (AZURE_FEDERATED_TOKEN_FILE, AZURE_CLIENT_ID, AZURE_TENANT_ID) is exchanged for an
//     Entra ID token and then for an ACR refresh token.
//
// Amazon ECR has its own API and is handled elsewhere.
type CloudAuth struct {
	HTTP *http.Client
	// Endpoints, overridable in tests.
	GCPMetadata string // default http://metadata.google.internal
	AzureLogin  string // default https://login.microsoftonline.com
	Scheme      string // for ACR's exchange endpoint; default https

	mu    sync.Mutex
	cache map[string]cloudToken
	noGCP time.Time // when the metadata server last could not be reached
}

type cloudToken struct {
	creds   Credentials
	expires time.Time
}

// NewCloudAuth returns a CloudAuth with short timeouts: off the cloud, the metadata
// server does not answer and the agent should move on quickly.
func NewCloudAuth() *CloudAuth {
	return &CloudAuth{HTTP: &http.Client{Timeout: 5 * time.Second}}
}

// ErrNoCloudIdentity means the host is a cloud registry but no identity is available.
var ErrNoCloudIdentity = errors.New("no cloud identity for this registry")

// IsGoogleRegistry reports whether host is Artifact Registry or Container Registry.
func IsGoogleRegistry(host string) bool {
	return strings.HasSuffix(host, "-docker.pkg.dev") || host == "gcr.io" || strings.HasSuffix(host, ".gcr.io")
}

// IsAzureRegistry reports whether host is an Azure Container Registry.
func IsAzureRegistry(host string) bool { return strings.HasSuffix(host, ".azurecr.io") }

// Credentials returns credentials for host from the cloud identity, ok false when
// host is no cloud registry this knows.
func (a *CloudAuth) Credentials(ctx context.Context, host string) (creds Credentials, ok bool, err error) {
	switch {
	case IsGoogleRegistry(host):
		creds, err = a.cached(host, func() (Credentials, time.Duration, error) { return a.google(ctx) })
	case IsAzureRegistry(host):
		creds, err = a.cached(host, func() (Credentials, time.Duration, error) { return a.azure(ctx, host) })
	default:
		return Credentials{}, false, nil
	}
	return creds, true, err
}

func (a *CloudAuth) cached(host string, fetch func() (Credentials, time.Duration, error)) (Credentials, error) {
	a.mu.Lock()
	if t, ok := a.cache[host]; ok && time.Now().Before(t.expires) {
		a.mu.Unlock()
		return t.creds, nil
	}
	a.mu.Unlock()
	creds, ttl, err := fetch()
	if err != nil {
		return Credentials{}, err
	}
	a.mu.Lock()
	if a.cache == nil {
		a.cache = map[string]cloudToken{}
	}
	a.cache[host] = cloudToken{creds: creds, expires: time.Now().Add(ttl - time.Minute)}
	a.mu.Unlock()
	return creds, nil
}

// google reads an access token for the instance's (or pod's) service account.
func (a *CloudAuth) google(ctx context.Context) (Credentials, time.Duration, error) {
	a.mu.Lock()
	recent := time.Since(a.noGCP) < 10*time.Minute
	a.mu.Unlock()
	if recent {
		return Credentials{}, 0, ErrNoCloudIdentity
	}
	base := a.GCPMetadata
	if base == "" {
		base = "http://metadata.google.internal"
		if h := os.Getenv("GCE_METADATA_HOST"); h != "" {
			base = "http://" + h
		}
	}
	//nolint:gosec // the GCE metadata server, or GCE_METADATA_HOST set by the operator
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/computeMetadata/v1/instance/service-accounts/default/token", nil)
	if err != nil {
		return Credentials{}, 0, err
	}
	req.Header.Set("Metadata-Flavor", "Google")
	resp, err := a.HTTP.Do(req) //nolint:gosec // see above
	if err != nil {
		a.mu.Lock()
		a.noGCP = time.Now()
		a.mu.Unlock()
		return Credentials{}, 0, fmt.Errorf("%w: GCE metadata server not reachable", ErrNoCloudIdentity)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return Credentials{}, 0, fmt.Errorf("%w: metadata server answered %d", ErrNoCloudIdentity, resp.StatusCode)
	}
	var tok struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&tok); err != nil || tok.AccessToken == "" {
		return Credentials{}, 0, fmt.Errorf("metadata token: %w", err)
	}
	return Credentials{Username: "oauth2accesstoken", Password: tok.AccessToken}, time.Duration(max(tok.ExpiresIn, 120)) * time.Second, nil
}

// azure exchanges the AKS Workload Identity token for an ACR refresh token.
func (a *CloudAuth) azure(ctx context.Context, host string) (Credentials, time.Duration, error) {
	file, client, tenant := os.Getenv("AZURE_FEDERATED_TOKEN_FILE"), os.Getenv("AZURE_CLIENT_ID"), os.Getenv("AZURE_TENANT_ID")
	if file == "" || client == "" || tenant == "" {
		return Credentials{}, 0, fmt.Errorf("%w: AKS Workload Identity is not set up (AZURE_FEDERATED_TOKEN_FILE, AZURE_CLIENT_ID, AZURE_TENANT_ID)", ErrNoCloudIdentity)
	}
	assertion, err := os.ReadFile(file) //nolint:gosec // the path AKS injects
	if err != nil {
		return Credentials{}, 0, err
	}
	login := a.AzureLogin
	if login == "" {
		login = "https://login.microsoftonline.com"
	}
	var aad struct {
		AccessToken string `json:"access_token"`
	}
	if err := a.postForm(ctx, login+"/"+url.PathEscape(tenant)+"/oauth2/v2.0/token", url.Values{
		"grant_type":            {"client_credentials"},
		"client_id":             {client},
		"scope":                 {"https://management.azure.com/.default"},
		"client_assertion_type": {"urn:ietf:params:oauth:client-assertion-type:jwt-bearer"},
		"client_assertion":      {strings.TrimSpace(string(assertion))},
	}, &aad); err != nil {
		return Credentials{}, 0, fmt.Errorf("entra id token: %w", err)
	}
	scheme := a.Scheme
	if scheme == "" {
		scheme = "https"
	}
	var acr struct {
		RefreshToken string `json:"refresh_token"`
	}
	if err := a.postForm(ctx, scheme+"://"+host+"/oauth2/exchange", url.Values{
		"grant_type":   {"access_token"},
		"service":      {host},
		"tenant":       {tenant},
		"access_token": {aad.AccessToken},
	}, &acr); err != nil {
		return Credentials{}, 0, fmt.Errorf("acr token exchange: %w", err)
	}
	// ACR accepts its refresh token as the password of this fixed user name.
	return Credentials{Username: "00000000-0000-0000-0000-000000000000", Password: acr.RefreshToken}, time.Hour, nil
}

func (a *CloudAuth) postForm(ctx context.Context, endpoint string, form url.Values, into any) error {
	//nolint:gosec // Entra ID's login endpoint, or the ACR registry being checked
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := a.HTTP.Do(req) //nolint:gosec // see above
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s answered %d", endpoint, resp.StatusCode)
	}
	return json.Unmarshal(body, into)
}
