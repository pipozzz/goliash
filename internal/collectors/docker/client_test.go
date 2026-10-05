// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: Apache-2.0

package docker

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

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

func TestConnectionHints(t *testing.T) {
	ctx := context.Background()
	c, _ := NewClient("tcp://goliash-no-such-host.invalid:2375")
	if err := c.Get(ctx, "/containers/json", nil); err == nil || !strings.Contains(err.Error(), "does not resolve where the collector runs") {
		t.Fatalf("dns: %v", err)
	}
	l, _ := (&net.ListenConfig{}).Listen(ctx, "tcp", "127.0.0.1:0")
	addr := l.Addr().String()
	_ = l.Close()
	c, _ = NewClient("tcp://" + addr)
	if err := c.Get(ctx, "/containers/json", nil); err == nil || !strings.Contains(err.Error(), "nothing listens there") {
		t.Fatalf("refused: %v", err)
	}
	c, _ = NewClient("unix:///nonexistent/docker.sock")
	if err := c.Get(ctx, "/containers/json", nil); err == nil || !strings.Contains(err.Error(), "socket is missing") {
		t.Fatalf("socket: %v", err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusForbidden) }))
	defer srv.Close()
	c, _ = NewClient("tcp://" + strings.TrimPrefix(srv.URL, "http://"))
	if err := c.Get(ctx, "/services", nil); err == nil || !strings.Contains(err.Error(), "allow CONTAINERS=1") {
		t.Fatalf("forbidden: %v", err)
	}
}
