// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

func TestAskCommand(t *testing.T) {
	var got struct{ auth, body string }
	answer := "data: {\"type\":\"tool\",\"tool\":\"updates\"}\n\n" +
		"data: {\"type\":\"tool\",\"tool\":\"updates\"}\n\n" +
		"data: {\"type\":\"text\",\"text\":\"Upgrade \"}\n\n" +
		"data: {\"type\":\"text\",\"text\":\"postgres first.\"}\n\n" +
		"data: {\"type\":\"done\"}\n\n"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got.auth, got.body = r.Header.Get("Authorization"), string(b)
		switch r.Header.Get("Authorization") {
		case "Bearer off":
			http.NotFound(w, r)
		case "Bearer broken":
			_, _ = io.WriteString(w, "data: {\"type\":\"error\",\"text\":\"The model is overloaded right now.\"}\n\n")
		default:
			_, _ = io.WriteString(w, answer)
		}
	}))
	defer srv.Close()
	ctx := context.Background()

	var out, errOut bytes.Buffer
	if err := askCmd(ctx, []string{"-server", srv.URL, "-token", "glsh_api_x", "what", "first?"}, strings.NewReader(""), &out, &errOut); err != nil {
		t.Fatal(err)
	}
	if out.String() != "Upgrade postgres first.\n" || errOut.String() != "· looking at updates\n" {
		t.Errorf("stdout %q, stderr %q", out.String(), errOut.String())
	}
	if got.auth != "Bearer glsh_api_x" || !strings.Contains(got.body, `"text":"what first?"`) {
		t.Errorf("sent %+v", got)
	}
	if err := askCmd(ctx, []string{"-server", srv.URL, "-token", "broken", "hi"}, strings.NewReader(""), &out, &errOut); err == nil || !strings.Contains(err.Error(), "overloaded") {
		t.Errorf("model error: %v", err)
	}
	if err := askCmd(ctx, []string{"-server", srv.URL, "-token", "off", "hi"}, strings.NewReader(""), &out, &errOut); err == nil || !strings.Contains(err.Error(), "Goli off") {
		t.Errorf("Goli off: %v", err)
	}
	if err := askCmd(ctx, []string{"-server", srv.URL, "-token", "x"}, strings.NewReader(""), &out, &errOut); err == nil {
		t.Error("asked nothing")
	}
}

func TestAskConversation(t *testing.T) {
	var bodies []string
	n := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(b))
		n++
		_, _ = io.WriteString(w, "data: {\"type\":\"text\",\"text\":\"answer "+strconv.Itoa(n)+"\"}\n\ndata: {\"type\":\"done\"}\n\n")
	}))
	defer srv.Close()
	var out, errOut bytes.Buffer
	in := strings.NewReader("and in staging?\n\nnot asked\n")
	if err := askCmd(context.Background(), []string{"-server", srv.URL, "-token", "t", "-i", "what runs in prod?"}, in, &out, &errOut); err != nil {
		t.Fatal(err)
	}
	if len(bodies) != 2 {
		t.Fatalf("%d questions asked", len(bodies))
	}
	// The second question carries the first and its answer.
	if !strings.Contains(bodies[1], `"text":"what runs in prod?"`) || !strings.Contains(bodies[1], `"role":"assistant","text":"answer 1"`) || !strings.Contains(bodies[1], `"text":"and in staging?"`) {
		t.Errorf("second request %s", bodies[1])
	}
	if !strings.Contains(out.String(), "answer 1") || !strings.Contains(out.String(), "answer 2") || !strings.Contains(errOut.String(), "goli> ") {
		t.Errorf("stdout %q stderr %q", out.String(), errOut.String())
	}
}
