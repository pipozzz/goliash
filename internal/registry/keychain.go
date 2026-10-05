// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: Apache-2.0

package registry

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Keychain holds registry credentials found where the platform keeps them: Docker's
// config.json and Kubernetes image pull secrets. A host may have several (two pull
// secrets for different projects on one Harbor); callers try them in order.
type Keychain map[string][]Credentials

// Lookup returns the credentials for a registry host.
func (k Keychain) Lookup(host string) []Credentials { return k[normalizeHost(host)] }

// Merge adds other's credentials after k's own, skipping duplicates.
func (k Keychain) Merge(other Keychain) {
	for host, creds := range other {
		for _, c := range creds {
			k.add(host, c)
		}
	}
}

func (k Keychain) add(host string, c Credentials) {
	host = normalizeHost(host)
	if host == "" || c.Password == "" {
		return
	}
	for _, have := range k[host] {
		if have == c {
			return
		}
	}
	k[host] = append(k[host], c)
}

// normalizeHost turns the keys Docker writes ("https://index.docker.io/v1/",
// "ghcr.io") into the host of a repository reference ("docker.io", "ghcr.io").
func normalizeHost(h string) string {
	h = strings.ToLower(strings.TrimSpace(h))
	h = strings.TrimPrefix(strings.TrimPrefix(h, "https://"), "http://")
	h, _, _ = strings.Cut(h, "/")
	switch h {
	case "index.docker.io", "registry-1.docker.io", "registry.hub.docker.com":
		return "docker.io"
	}
	return h
}

// ParseDockerConfig reads a Docker config.json (also the content of a Kubernetes
// kubernetes.io/dockerconfigjson secret) or the older .dockercfg format. Entries kept
// by a credential helper (credsStore, credHelpers) hold no secret and are skipped.
func ParseDockerConfig(b []byte) (Keychain, error) {
	type entry struct {
		Auth     string `json:"auth"`
		Username string `json:"username"`
		Password string `json:"password"`
		Token    string `json:"identitytoken"`
	}
	var cfg struct {
		Auths map[string]entry `json:"auths"`
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
		return nil, fmt.Errorf("docker config: %w", err)
	}
	if cfg.Auths == nil {
		// .dockercfg: the hosts are the top-level keys.
		var legacy map[string]entry
		if err := json.Unmarshal(b, &legacy); err == nil {
			cfg.Auths = legacy
		}
	}
	k := Keychain{}
	for host, e := range cfg.Auths {
		c := Credentials{Username: e.Username, Password: e.Password}
		if e.Auth != "" {
			raw, err := base64.StdEncoding.DecodeString(e.Auth)
			if err != nil {
				return nil, fmt.Errorf("docker config: auth of %s is not base64", host)
			}
			c = ParseCredentials(string(raw))
		}
		if c.Password == "" && e.Token != "" {
			c = Credentials{Username: "<token>", Password: e.Token}
		}
		k.add(host, c)
	}
	return k, nil
}

// DockerConfigKeychain reads $DOCKER_CONFIG/config.json, or ~/.docker/config.json
// without DOCKER_CONFIG. A missing file is an empty keychain.
func DockerConfigKeychain() (Keychain, error) {
	dir := os.Getenv("DOCKER_CONFIG")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return Keychain{}, nil //nolint:nilerr // no home directory: nothing configured
		}
		dir = filepath.Join(home, ".docker")
	}
	b, err := os.ReadFile(filepath.Join(dir, "config.json")) //nolint:gosec // the user's own Docker configuration
	if os.IsNotExist(err) {
		return Keychain{}, nil
	}
	if err != nil {
		return nil, err
	}
	return ParseDockerConfig(b)
}
