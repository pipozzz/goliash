// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: Apache-2.0

package registry

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestParseDockerConfig(t *testing.T) {
	auth := base64.StdEncoding.EncodeToString([]byte("bob:s3cret"))
	k, err := ParseDockerConfig([]byte(`{
		"auths": {
			"https://index.docker.io/v1/": {"auth": "` + auth + `"},
			"harbor.example.com": {"username": "robot$ci", "password": "pw"},
			"acr.example.io": {"identitytoken": "tok"},
			"helper.example.com": {}
		},
		"credsStore": "desktop"
	}`))
	if err != nil {
		t.Fatal(err)
	}
	for host, want := range map[string][]Credentials{
		"docker.io":          {{Username: "bob", Password: "s3cret"}},
		"harbor.example.com": {{Username: "robot$ci", Password: "pw"}},
		"acr.example.io":     {{Username: "<token>", Password: "tok"}},
		"helper.example.com": nil,
	} {
		if got := k.Lookup(host); !reflect.DeepEqual(got, want) {
			t.Errorf("%s: %+v, want %+v", host, got, want)
		}
	}

	legacy, err := ParseDockerConfig([]byte(`{"https://ghcr.io": {"auth": "` + auth + `"}}`))
	if err != nil || len(legacy.Lookup("ghcr.io")) != 1 {
		t.Fatalf(".dockercfg: %+v %v", legacy, err)
	}
	if _, err := ParseDockerConfig([]byte(`{"auths": {"x": {"auth": "%%"}}}`)); err == nil {
		t.Error("bad base64 accepted")
	}

	legacy.Merge(Keychain{"ghcr.io": {{Username: "bob", Password: "s3cret"}, {Username: "ann", Password: "x"}}})
	if got := legacy.Lookup("GHCR.io"); len(got) != 2 || got[1].Username != "ann" {
		t.Errorf("merge: %+v", got)
	}
}

func TestDockerConfigKeychain(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("DOCKER_CONFIG", dir)
	if k, err := DockerConfigKeychain(); err != nil || len(k) != 0 {
		t.Fatalf("missing file: %v %v", k, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{"auths":{"r.example.com":{"username":"u","password":"p"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if k, err := DockerConfigKeychain(); err != nil || len(k.Lookup("r.example.com")) != 1 {
		t.Fatalf("%v %v", k, err)
	}
}
