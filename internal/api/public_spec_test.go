// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/getkin/kin-openapi/routers/legacy"

	spec "github.com/pipozzz/goliash/api"
)

// Every documented endpoint answers as public-v1.yaml says, errors included.
func TestPublicAPIMatchesSpec(t *testing.T) {
	ctx := context.Background()
	doc, err := openapi3.NewLoader().LoadFromData(spec.PublicV1)
	if err != nil {
		t.Fatal(err)
	}
	if err := doc.Validate(ctx); err != nil {
		t.Fatalf("spec invalid: %v", err)
	}
	doc.Servers = openapi3.Servers{{URL: "/"}}
	router, err := legacy.NewRouter(doc)
	if err != nil {
		t.Fatal(err)
	}
	e := newPublicEnv(t)

	covered := map[string]bool{}
	for _, c := range []struct {
		method, path, token, body string
		status                    int
	}{
		{"GET", "/api/v1/matrix", e.token, "", 200},
		{"GET", "/api/v1/matrix?at=2020-01-01T00:00", e.token, "", 200},
		{"GET", "/api/v1/matrix?at=yesterday", e.token, "", 400},
		{"GET", "/api/v1/inventory", e.token, "", 200},
		{"GET", "/api/v1/hygiene", e.token, "", 200},
		{"GET", "/api/v1/inventory?format=csv", e.token, "", 200},
		{"GET", "/api/v1/services", e.token, "", 200},
		{"GET", "/api/v1/environments", e.token, "", 200},
		{"GET", "/api/v1/targets", e.token, "", 200},
		{"GET", "/api/v1/events?service=web&limit=5", e.token, "", 200},
		{"GET", "/api/v1/events?before=yesterday", e.token, "", 400},
		{"GET", "/api/v1/events?since=2h", e.token, "", 200},
		{"GET", "/api/v1/events?since=soon", e.token, "", 400},
		{"GET", "/api/v1/events?service=missing", e.token, "", 404},
		{"GET", "/api/v1/drifts", e.token, "", 200},
		{"GET", "/api/v1/promotions", e.token, "", 200},
		{"GET", "/api/v1/delivery?window=7d", e.token, "", 200},
		{"GET", "/api/v1/delivery?window=soon", e.token, "", 400},
		{"GET", "/api/v1/matrix", "", "", 401},
		{"POST", "/api/v1/acks", e.token, `{"service":"web","kind":"drift","environment":"prod","until":"2030-01-01T00:00:00Z"}`, 201},
		{"POST", "/api/v1/acks", e.token, `{"service":"web","kind":"release"}`, 400},
		{"GET", "/api/v1/openapi.yaml", "", "", 200},
	} {
		req, _ := http.NewRequestWithContext(ctx, c.method, e.srv.URL+c.path, strings.NewReader(c.body))
		if c.token != "" {
			req.Header.Set("Authorization", "Bearer "+c.token)
		}
		if c.body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != c.status {
			t.Fatalf("%s %s: %d, want %d: %s", c.method, c.path, resp.StatusCode, c.status, body)
		}

		specReq, _ := http.NewRequestWithContext(ctx, c.method, c.path, strings.NewReader(c.body))
		route, params, err := router.FindRoute(specReq)
		if err != nil {
			t.Fatalf("%s %s is not in the spec: %v", c.method, c.path, err)
		}
		covered[c.method+" "+route.Path] = true
		in := &openapi3filter.ResponseValidationInput{
			RequestValidationInput: &openapi3filter.RequestValidationInput{Request: specReq, PathParams: params, Route: route},
			Status:                 resp.StatusCode, Header: resp.Header, Body: io.NopCloser(bytes.NewReader(body)),
			Options: &openapi3filter.Options{IncludeResponseStatus: true},
		}
		if strings.HasSuffix(c.path, ".yaml") {
			in.Options.ExcludeResponseBody = true
		}
		if err := openapi3filter.ValidateResponse(ctx, in); err != nil {
			t.Fatalf("%s %s: response violates the spec: %v\n%s", c.method, c.path, err, body)
		}
	}
	for path, item := range doc.Paths.Map() {
		for method := range item.Operations() {
			if !covered[method+" "+path] {
				t.Errorf("%s %s is documented but not tested", method, path)
			}
		}
	}
}
