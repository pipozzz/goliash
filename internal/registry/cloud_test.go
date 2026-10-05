// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: Apache-2.0

package registry

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCloudHosts(t *testing.T) {
	for host, want := range map[string][2]bool{
		"europe-west1-docker.pkg.dev": {true, false},
		"gcr.io":                      {true, false},
		"eu.gcr.io":                   {true, false},
		"acme.azurecr.io":             {false, true},
		"ghcr.io":                     {false, false},
	} {
		if IsGoogleRegistry(host) != want[0] || IsAzureRegistry(host) != want[1] {
			t.Errorf("%s: google %v azure %v", host, IsGoogleRegistry(host), IsAzureRegistry(host))
		}
	}
	a := NewCloudAuth()
	if _, ok, err := a.Credentials(context.Background(), "ghcr.io"); ok || err != nil {
		t.Errorf("ghcr.io treated as a cloud registry: %v %v", ok, err)
	}
}

func TestGoogleCredentials(t *testing.T) {
	calls := 0
	md := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("Metadata-Flavor") != "Google" || !strings.HasSuffix(r.URL.Path, "/service-accounts/default/token") {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		_, _ = w.Write([]byte(`{"access_token":"ya29.token","expires_in":3599}`))
	}))
	t.Cleanup(md.Close)
	a := NewCloudAuth()
	a.GCPMetadata = md.URL
	for range 2 {
		creds, ok, err := a.Credentials(context.Background(), "europe-west1-docker.pkg.dev")
		if !ok || err != nil || creds.Username != "oauth2accesstoken" || creds.Password != "ya29.token" {
			t.Fatalf("%+v %v %v", creds, ok, err)
		}
	}
	if calls != 1 {
		t.Fatalf("token not cached: %d calls", calls)
	}

	off := NewCloudAuth()
	off.GCPMetadata = "http://127.0.0.1:1" // nothing listens: not on Google Cloud
	if _, ok, err := off.Credentials(context.Background(), "gcr.io"); !ok || !errors.Is(err, ErrNoCloudIdentity) {
		t.Fatalf("off the cloud: %v %v", ok, err)
	}
}

func TestAzureCredentials(t *testing.T) {
	login := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.URL.Path != "/tenant-1/oauth2/v2.0/token" || r.PostForm.Get("client_assertion") != "federated-jwt" || r.PostForm.Get("client_id") != "client-1" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"access_token":"aad-token"}`))
	}))
	t.Cleanup(login.Close)
	acr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.URL.Path != "/oauth2/exchange" || r.PostForm.Get("access_token") != "aad-token" || r.PostForm.Get("grant_type") != "access_token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"refresh_token":"acr-refresh"}`))
	}))
	t.Cleanup(acr.Close)

	a := NewCloudAuth()
	if _, ok, err := a.Credentials(context.Background(), "acme.azurecr.io"); !ok || !errors.Is(err, ErrNoCloudIdentity) {
		t.Fatalf("without workload identity: %v %v", ok, err)
	}
	file := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(file, []byte("federated-jwt\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AZURE_FEDERATED_TOKEN_FILE", file)
	t.Setenv("AZURE_CLIENT_ID", "client-1")
	t.Setenv("AZURE_TENANT_ID", "tenant-1")
	a.AzureLogin, a.Scheme = login.URL, "http"
	// The fake ACR stands in for the registry host; a real one is <name>.azurecr.io.
	creds, _, err := a.azure(context.Background(), strings.TrimPrefix(acr.URL, "http://"))
	if err != nil || creds.Username != "00000000-0000-0000-0000-000000000000" || creds.Password != "acr-refresh" {
		t.Fatalf("%+v %v", creds, err)
	}
}
