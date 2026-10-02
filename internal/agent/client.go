// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/pipozzz/goliash/pkg/agentproto"
	"github.com/pipozzz/goliash/pkg/buildinfo"
)

// statusError is a non-success response from the server.
type statusError struct {
	Code       int
	Detail     string
	RetryAfter time.Duration
}

func (e *statusError) Error() string {
	if e.Detail != "" {
		return fmt.Sprintf("server answered %d: %s", e.Code, e.Detail)
	}
	return fmt.Sprintf("server answered %d", e.Code)
}

// retryable reports whether a request that failed with err may succeed later as is.
// Network errors, 429 and 5xx are retryable; other responses are not.
func retryable(err error) bool {
	var se *statusError
	if !errors.As(err, &se) {
		return true
	}
	return se.Code == http.StatusTooManyRequests || se.Code >= 500
}

// fatal reports whether err means the agent cannot work at all: its token was rejected
// or its version is no longer supported.
func fatal(err error) bool {
	var se *statusError
	return errors.As(err, &se) && (se.Code == http.StatusUnauthorized || se.Code == http.StatusUpgradeRequired)
}

func statusCode(err error) int {
	var se *statusError
	if errors.As(err, &se) {
		return se.Code
	}
	return 0
}

// client wraps the generated protocol client with auth and error handling.
type client struct {
	api *agentproto.ClientWithResponses
}

func newClient(serverURL, token string, hc *http.Client) (*client, error) {
	if serverURL == "" {
		return nil, errors.New("server URL is required")
	}
	if !strings.HasPrefix(token, "glsh_agent_") {
		return nil, errors.New("agent token must start with glsh_agent_")
	}
	api, err := agentproto.NewClientWithResponses(strings.TrimSuffix(serverURL, "/"),
		agentproto.WithHTTPClient(hc),
		agentproto.WithRequestEditorFn(func(_ context.Context, req *http.Request) error {
			req.Header.Set("Authorization", "Bearer "+token)
			req.Header.Set("User-Agent", "goliash-agent/"+buildinfo.Version)
			return nil
		}))
	if err != nil {
		return nil, err
	}
	return &client{api: api}, nil
}

func (c *client) register(ctx context.Context, req agentproto.RegisterRequest) (agentproto.RegisterResponse, error) {
	resp, err := c.api.RegisterAgentWithResponse(ctx, req)
	if err != nil {
		return agentproto.RegisterResponse{}, err
	}
	if resp.JSON200 == nil {
		return agentproto.RegisterResponse{}, toError(resp.HTTPResponse, resp.Body)
	}
	return *resp.JSON200, nil
}

// config fetches the configuration. It returns changed=false when etag is still current.
func (c *client) config(ctx context.Context, etag string) (cfg agentproto.AgentConfig, newETag string, changed bool, err error) {
	params := &agentproto.GetConfigParams{}
	if etag != "" {
		params.IfNoneMatch = &etag
	}
	resp, err := c.api.GetConfigWithResponse(ctx, params)
	if err != nil {
		return cfg, "", false, err
	}
	switch {
	case resp.StatusCode() == http.StatusNotModified:
		return cfg, etag, false, nil
	case resp.JSON200 != nil:
		return *resp.JSON200, resp.HTTPResponse.Header.Get("ETag"), true, nil
	default:
		return cfg, "", false, toError(resp.HTTPResponse, resp.Body)
	}
}

// snapshot sends a gzip-compressed snapshot body.
func (c *client) snapshot(ctx context.Context, gz []byte) (agentproto.SnapshotAck, error) {
	resp, err := c.api.PostSnapshotWithBodyWithResponse(ctx, "application/json", bytes.NewReader(gz),
		func(_ context.Context, req *http.Request) error {
			req.Header.Set("Content-Encoding", "gzip")
			return nil
		})
	if err != nil {
		return agentproto.SnapshotAck{}, err
	}
	if resp.JSON202 == nil {
		return agentproto.SnapshotAck{}, toError(resp.HTTPResponse, resp.Body)
	}
	return *resp.JSON202, nil
}

func (c *client) heartbeat(ctx context.Context, hb agentproto.Heartbeat) (agentproto.HeartbeatResponse, error) {
	resp, err := c.api.PostHeartbeatWithResponse(ctx, hb)
	if err != nil {
		return agentproto.HeartbeatResponse{}, err
	}
	if resp.JSON200 == nil {
		return agentproto.HeartbeatResponse{}, toError(resp.HTTPResponse, resp.Body)
	}
	return *resp.JSON200, nil
}

func toError(resp *http.Response, body []byte) error {
	se := &statusError{Code: resp.StatusCode}
	var p agentproto.Problem
	if err := json.Unmarshal(body, &p); err == nil && p.Detail != nil {
		se.Detail = *p.Detail
	} else if err == nil && p.Title != "" {
		se.Detail = p.Title
	}
	if s := resp.Header.Get("Retry-After"); s != "" {
		if n, err := strconv.Atoi(s); err == nil && n > 0 {
			se.RetryAfter = time.Duration(n) * time.Second
		}
	}
	return se
}
