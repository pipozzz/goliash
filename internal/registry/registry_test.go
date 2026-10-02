// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: Apache-2.0

package registry

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// fakeRegistry speaks the Docker Hub style token flow with paginated tags.
func fakeRegistry(t *testing.T, requireUser string) (*httptest.Server, *atomic.Int32) {
	var tokenCalls atomic.Int32
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("registry got %s", r.Method)
		}
		switch r.URL.Path {
		case "/token":
			tokenCalls.Add(1)
			if r.URL.Query().Get("scope") != "repository:acme/app:pull" || r.URL.Query().Get("service") != "fake" {
				t.Errorf("token query %s", r.URL.RawQuery)
			}
			if requireUser != "" {
				if u, p, ok := r.BasicAuth(); !ok || u != requireUser || p != "secret" {
					w.WriteHeader(http.StatusUnauthorized)
					return
				}
			}
			_, _ = w.Write([]byte(`{"token":"T0K","expires_in":300}`))
		case "/v2/acme/app/tags/list":
			if r.Header.Get("Authorization") != "Bearer T0K" {
				w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Bearer realm="%s/token",service="fake",scope="repository:acme/app:pull"`, srv.URL))
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			if r.URL.Query().Get("last") == "" {
				w.Header().Set("Link", `</v2/acme/app/tags/list?last=1.1.0&n=1000>; rel="next"`)
				_, _ = w.Write([]byte(`{"name":"acme/app","tags":["1.0.0","1.1.0"]}`))
				return
			}
			_, _ = w.Write([]byte(`{"name":"acme/app","tags":["1.2.0","latest"]}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &tokenCalls
}

func client() *Client {
	c := New()
	c.Scheme = "http"
	return c
}

func TestListTagsWithTokenAndPages(t *testing.T) {
	srv, tokenCalls := fakeRegistry(t, "")
	c := client()
	repo := strings.TrimPrefix(srv.URL, "http://") + "/acme/app"
	for range 2 {
		tags, err := c.ListTags(context.Background(), repo, Credentials{})
		if err != nil {
			t.Fatal(err)
		}
		if strings.Join(tags, ",") != "1.0.0,1.1.0,1.2.0,latest" {
			t.Fatalf("tags %v", tags)
		}
	}
	if tokenCalls.Load() != 1 {
		t.Fatalf("token fetched %d times; it should be cached", tokenCalls.Load())
	}
}

func TestPrivateRepository(t *testing.T) {
	srv, _ := fakeRegistry(t, "robot")
	repo := strings.TrimPrefix(srv.URL, "http://") + "/acme/app"
	if _, err := client().ListTags(context.Background(), repo, Credentials{}); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("anonymous: %v", err)
	}
	tags, err := client().ListTags(context.Background(), repo, ParseCredentials("robot:secret"))
	if err != nil || len(tags) != 4 {
		t.Fatalf("with credentials: %v %v", tags, err)
	}
}

func TestBasicAuthRegistry(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if u, p, ok := r.BasicAuth(); !ok || u != "u" || p != "p" {
			w.Header().Set("WWW-Authenticate", `Basic realm="registry"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"tags":["2.0"]}`))
	}))
	defer srv.Close()
	repo := strings.TrimPrefix(srv.URL, "http://") + "/team/api"
	if tags, err := client().ListTags(context.Background(), repo, Credentials{Username: "u", Password: "p"}); err != nil || tags[0] != "2.0" {
		t.Fatalf("%v %v", tags, err)
	}
	if _, err := client().ListTags(context.Background(), repo, Credentials{}); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("anonymous basic: %v", err)
	}
}

func TestParseCredentials(t *testing.T) {
	if c := ParseCredentials("user:pa:ss"); c.Username != "user" || c.Password != "pa:ss" {
		t.Fatalf("%+v", c)
	}
	if c := ParseCredentials("tok"); c.Username != "" || c.Password != "tok" {
		t.Fatalf("%+v", c)
	}
}
