// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package ui

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/pipozzz/goliash/internal/goli"
	"github.com/pipozzz/goliash/internal/store"
)

// SlackConfig lets a Slack app's /goli command ask Goli. Goli sees what Token may see.
type SlackConfig struct {
	SigningSecret string // the Slack app's signing secret: every request is checked against it
	Token         string // a Goliash API token (glsh_api_…), viewer is enough
	InChannel     bool   // answers go to the channel; otherwise only the asker sees them
}

// slackHooks is where Slack's response URLs live; nothing else is ever posted to.
const slackHooks = "https://hooks.slack.com/"

// goliSlack answers Slack's slash command: at once that Goli is looking, then the answer through the command's
// response URL, as Slack allows only three seconds for the first.
func (s *Server) goliSlack(w http.ResponseWriter, r *http.Request) {
	cfg := s.slack
	if s.goli == nil || cfg == nil {
		http.NotFound(w, r)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 64<<10))
	if err != nil {
		http.Error(w, "unreadable request", http.StatusBadRequest)
		return
	}
	if !slackSigned(cfg.SigningSecret, r.Header, body, time.Now()) {
		http.Error(w, "the request is not signed by the Slack app", http.StatusUnauthorized)
		return
	}
	form, err := url.ParseQuery(string(body))
	if err != nil {
		http.Error(w, "unreadable form", http.StatusBadRequest)
		return
	}
	question := strings.TrimSpace(form.Get("text"))
	reply := func(text string) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"response_type": "ephemeral", "text": text})
	}
	if question == "" {
		reply("Ask Goli about what runs where, e.g. `/goli what changed in prod in the last two hours?`")
		return
	}
	if len([]rune(question)) > maxQuestion {
		reply(fmt.Sprintf("Ask in at most %d characters.", maxQuestion))
		return
	}
	hook := form.Get("response_url")
	if !strings.HasPrefix(hook, s.slackHooks) {
		http.Error(w, "unexpected response_url", http.StatusBadRequest)
		return
	}
	// Goli asks as the configured token, with its workspace and role.
	asToken, _ := http.NewRequestWithContext(context.WithoutCancel(r.Context()), http.MethodPost, "/goli/ask", nil)
	asToken.Header.Set("Authorization", "Bearer "+cfg.Token)
	p, ok, err := s.auth.Authenticate(asToken)
	if err != nil || !ok || !p.Can(store.RoleViewer) {
		reply("Goli cannot read Goliash: the server's GOLIASH_SLACK_GOLI_TOKEN was refused.")
		return
	}
	user := form.Get("user_id")
	if !s.goli.Allow("slack:"+form.Get("team_id")+":"+user, time.Now()) {
		reply("You asked Goli a lot this hour; try again later.")
		return
	}
	if !s.goli.AllowToday(time.Now()) {
		reply("Goli answered as many questions today as this server allows; ask again tomorrow.")
		return
	}
	reply("Goli is looking: " + slackEscape(question))

	// The answer outlives this request: Slack has its reply already.
	go s.goliSlackAnswer(context.WithoutCancel(r.Context()), asToken, p.Scope, user, question, hook)
}

func (s *Server) goliSlackAnswer(parent context.Context, asToken *http.Request, sc store.Scope, user, question, hook string) {
	ctx, cancel := context.WithTimeout(parent, 3*time.Minute)
	defer cancel()
	workspace := ""
	if wss, err := s.store.ListWorkspaces(ctx); err == nil {
		for _, ws := range wss {
			if ws.ID == sc.WorkspaceID {
				workspace = ws.Name
			}
		}
	}
	var answer strings.Builder
	var problem string
	looked := []string{}
	used, err := func() ([]string, error) {
		tools, err := goli.NewMCPTools(ctx, s.api, asToken.WithContext(ctx))
		if err != nil {
			return nil, err
		}
		defer func() { _ = tools.Close() }()
		return s.goli.Ask(ctx, tools, []goli.Turn{{Role: "user", Text: question}}, goli.Context{
			Workspace: workspace, Role: "viewer", Now: time.Now().UTC().Format(time.RFC3339), Page: "Slack",
		}, func(e goli.Event) {
			switch e.Type {
			case "text":
				answer.WriteString(e.Text)
			case "error":
				problem = e.Text
			}
		})
	}()
	for _, t := range used {
		if !contains(looked, t) {
			looked = append(looked, t)
		}
	}
	if err != nil {
		problem = goli.Describe(err)
	}
	s.log.InfoContext(ctx, "goli answered in Slack", "workspace", workspace, "tools", used, "err", err)

	text := slackText(answer.String(), s.publicURL)
	if problem != "" {
		text = strings.TrimSpace(text + "\n\n_" + slackEscape(problem) + "_")
	}
	if text == "" {
		text = "_Goli had nothing to say._"
	}
	var blocks []map[string]any
	for _, chunk := range slackChunks(text, 2900) {
		blocks = append(blocks, map[string]any{"type": "section", "text": map[string]any{"type": "mrkdwn", "text": chunk}})
	}
	meta := "Asked by <@" + user + ">: " + slackEscape(truncateRunes(question, 200))
	if len(looked) > 0 {
		meta += " · Goli looked at " + strings.Join(looked, ", ")
	}
	if s.publicURL != "" {
		meta += " · <" + s.publicURL + "/|Open Goliash>"
	}
	blocks = append(blocks, map[string]any{"type": "context", "elements": []any{map[string]any{"type": "mrkdwn", "text": meta}}})
	responseType := "ephemeral"
	if s.slack.InChannel {
		responseType = "in_channel"
	}
	var msg bytes.Buffer
	enc := json.NewEncoder(&msg)
	enc.SetEscapeHTML(false) // Slack's own <link|label> markup
	_ = enc.Encode(map[string]any{"response_type": responseType, "replace_original": true, "text": truncateRunes(text, 3000), "blocks": blocks})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, hook, &msg)
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.slackHTTP.Do(req)
	if err != nil {
		s.log.WarnContext(ctx, "goli: posting to Slack failed", "err", err)
		return
	}
	_ = resp.Body.Close()
}

// slackSigned checks Slack's request signature: v0=HMAC-SHA256(secret, "v0:" + timestamp + ":" + body), with a
// timestamp within five minutes so an old request cannot be replayed.
func slackSigned(secret string, h http.Header, body []byte, now time.Time) bool {
	ts := h.Get("X-Slack-Request-Timestamp")
	sec, err := strconv.ParseInt(ts, 10, 64)
	if secret == "" || err != nil || now.Sub(time.Unix(sec, 0)).Abs() > 5*time.Minute {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte("v0:" + ts + ":"))
	_, _ = mac.Write(body)
	want := "v0=" + hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(want), []byte(h.Get("X-Slack-Signature")))
}

func slackEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
}

var (
	mdLink  = regexp.MustCompile(`\[([^\]]+)\]\(([^)\s]+)\)`)
	mdBold  = regexp.MustCompile(`\*\*([^*]+)\*\*`)
	mdHead  = regexp.MustCompile(`(?m)^#{1,6}\s+(.+)$`)
	mdItem  = regexp.MustCompile(`(?m)^(\s*)[-*]\s+`)
	badLink = regexp.MustCompile(`[|<>]`)
)

// slackText turns Goli's Markdown into Slack's mrkdwn: links to Goliash become absolute, bold and headings
// become Slack bold, list bullets become dots. Everything else is escaped as text.
func slackText(md, base string) string {
	t := slackEscape(strings.TrimSpace(md))
	t = mdLink.ReplaceAllStringFunc(t, func(m string) string {
		p := mdLink.FindStringSubmatch(m)
		label, href := p[1], strings.ReplaceAll(p[2], "&amp;", "&")
		switch {
		case strings.HasPrefix(href, "/") && !strings.HasPrefix(href, "//") && base != "":
			href = base + href
		case strings.HasPrefix(href, "https://"):
		default:
			return label
		}
		if badLink.MatchString(href) {
			return label
		}
		return "<" + href + "|" + strings.ReplaceAll(label, "|", "/") + ">"
	})
	t = mdBold.ReplaceAllString(t, "*$1*")
	t = mdHead.ReplaceAllString(t, "*$1*")
	t = mdItem.ReplaceAllString(t, "$1• ")
	return t
}

// slackChunks splits text at line ends into pieces Slack accepts in one block.
func slackChunks(text string, limit int) []string {
	var out []string
	for len([]rune(text)) > limit {
		r := []rune(text)
		cut := strings.LastIndex(string(r[:limit]), "\n")
		if cut <= 0 {
			cut = len(string(r[:limit]))
		}
		out = append(out, strings.TrimSpace(text[:cut]))
		text = strings.TrimSpace(text[cut:])
	}
	if text != "" {
		out = append(out, text)
	}
	return out
}

func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
