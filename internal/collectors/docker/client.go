// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: Apache-2.0

package docker

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"time"
)

// Client talks to the Docker Engine API with GET requests only.
type Client struct {
	base string
	http *http.Client
}

// NewClient connects to host: tcp://host:port (normally a docker-socket-proxy that
// only allows GET), https://host:port or unix:///path/to/docker.sock.
func NewClient(host string) (*Client, error) {
	u, err := url.Parse(host)
	if err != nil {
		return nil, fmt.Errorf("docker_host: %w", err)
	}
	hc := &http.Client{Timeout: 30 * time.Second}
	switch u.Scheme {
	case "tcp", "http":
		return &Client{base: "http://" + u.Host, http: hc}, nil
	case "https":
		return &Client{base: "https://" + u.Host, http: hc}, nil
	case "unix":
		socket := u.Path
		hc.Transport = &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", socket)
		}}
		return &Client{base: "http://docker", http: hc}, nil
	}
	return nil, fmt.Errorf("docker_host %q: use tcp://, https:// or unix://", host)
}

// Get decodes the JSON response of GET path into v.
func (c *Client) Get(ctx context.Context, path string, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+path, nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("docker API: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		var msg struct{ Message string }
		if json.Unmarshal(body, &msg) == nil && msg.Message != "" {
			return fmt.Errorf("docker API GET %s: %d %s", path, resp.StatusCode, msg.Message)
		}
		return fmt.Errorf("docker API GET %s: %d", path, resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(v)
}
