// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: Apache-2.0

package docker

import "testing"

func TestDockerHost(t *testing.T) {
	for host, want := range map[string]string{
		"tcp://proxy:2375":            "http://proxy:2375",
		"https://docker.example:2376": "https://docker.example:2376",
		"unix:///var/run/docker.sock": "http://docker",
	} {
		c, err := NewClient(host)
		if err != nil || c.base != want {
			t.Errorf("%s -> %+v, %v", host, c, err)
		}
	}
	if _, err := NewClient("ssh://host"); err == nil {
		t.Error("ssh accepted")
	}
}
