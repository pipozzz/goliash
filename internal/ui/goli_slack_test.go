// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package ui

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/pipozzz/goliash/internal/api"
	"github.com/pipozzz/goliash/internal/goli"
	"github.com/pipozzz/goliash/internal/store"
	"github.com/pipozzz/goliash/internal/tokens"
)

func slackSign(secret string, ts time.Time, body string) http.Header {
	h := http.Header{}
	t := strconv.FormatInt(ts.Unix(), 10)
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte("v0:" + t + ":" + body))
	h.Set("X-Slack-Request-Timestamp", t)
	h.Set("X-Slack-Signature", "v0="+hex.EncodeToString(mac.Sum(nil)))
	return h
}

func TestSlackSignature(t *testing.T) {
	now := time.Now()
	body := []byte("text=hi")
	if !slackSigned("s3cret", slackSign("s3cret", now, "text=hi"), body, now) {
		t.Error("a good signature refused")
	}
	if slackSigned("s3cret", slackSign("other", now, "text=hi"), body, now) {
		t.Error("a wrong secret accepted")
	}
	if slackSigned("s3cret", slackSign("s3cret", now.Add(-10*time.Minute), "text=hi"), body, now) {
		t.Error("an old request accepted")
	}
	if slackSigned("s3cret", slackSign("s3cret", now, "text=hi"), []byte("text=bye"), now) {
		t.Error("a changed body accepted")
	}
	if slackSigned("", slackSign("", now, "text=hi"), body, now) {
		t.Error("no secret accepted")
	}
}

func TestSlackText(t *testing.T) {
	got := slackText("## Upgrade first\n**postgres** in prod: see [Updates](/updates?env=prod&x=1) and [notes](https://example.com/n).\n- a <b> & c\n* [bad](javascript:alert(1))", "https://goliash.example.com")
	for _, want := range []string{
		"*Upgrade first*",
		"*postgres* in prod: see <https://goliash.example.com/updates?env=prod&x=1|Updates> and <https://example.com/n|notes>.",
		"• a &lt;b&gt; &amp; c",
		"• bad",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in\n%s", want, got)
		}
	}
	if strings.Contains(got, "javascript") {
		t.Errorf("unsafe link kept:\n%s", got)
	}
	if c := slackChunks(strings.Repeat("line of text\n", 600), 2900); len(c) < 3 {
		t.Errorf("%d chunks", len(c))
	}
}

func TestGoliInSlack(t *testing.T) {
	e := newUIEnv(t)
	ctx := context.Background()
	raw, hash := tokens.New(tokens.API)
	if _, err := e.st.CreateAPIToken(ctx, e.ws.Scope(), store.APIToken{Name: "slack", Role: store.RoleViewer}, hash); err != nil {
		t.Fatal(err)
	}
	claude := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, goliTextTurn)
	}))
	defer claude.Close()
	posted := make(chan string, 1)
	hooks := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		posted <- string(b)
	}))
	defer hooks.Close()
	log := slog.New(slog.DiscardHandler)
	apiMux := http.NewServeMux()
	api.NewPublicHandler(e.st, e.auth, log).Register(apiMux)
	e.ui.goli = goli.New(goli.Config{APIKey: "sk-test", Model: goli.DefaultModel, BaseURL: claude.URL, Effort: "medium", PerHour: 5}, log)
	e.ui.api = apiMux
	e.ui.slack = &SlackConfig{SigningSecret: "s3cret", Token: raw}
	e.ui.slackHooks = hooks.URL + "/"

	send := func(body string, h http.Header) (int, string) {
		req, _ := http.NewRequestWithContext(ctx, http.MethodPost, e.srv.URL+"/integrations/slack/goli", strings.NewReader(body))
		req.Header = h
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	form := url.Values{"text": {"Which nginx runs in prod?"}, "user_id": {"U1"}, "team_id": {"T1"}, "response_url": {hooks.URL + "/commands/1"}}.Encode()

	if code, _ := send(form, slackSign("wrong", time.Now(), form)); code != http.StatusUnauthorized {
		t.Errorf("bad signature: %d", code)
	}
	evil := url.Values{"text": {"x"}, "response_url": {"http://169.254.169.254/latest"}}.Encode()
	if code, _ := send(evil, slackSign("s3cret", time.Now(), evil)); code != http.StatusBadRequest {
		t.Errorf("foreign response_url: %d", code)
	}
	code, body := send(form, slackSign("s3cret", time.Now(), form))
	if code != http.StatusOK || !strings.Contains(body, "Goli is looking") || !strings.Contains(body, `"ephemeral"`) {
		t.Fatalf("%d %s", code, body)
	}
	select {
	case msg := <-posted:
		if !strings.Contains(msg, "nginx runs 1.27.2 in prod.") || !strings.Contains(msg, "<@U1>") || !strings.Contains(msg, `"replace_original":true`) {
			t.Errorf("answer %s", msg)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("no answer posted to Slack")
	}
}
