// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package notifier

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"strings"

	"github.com/pipozzz/goliash/internal/store"
)

// lines renders a message as a title (for digests) and one line per item; link formats
// an item's release notes link in the target's markup.
func lines(msg Message, link func(text, url string) string) (title string, body []string) {
	if msg.Digest || len(msg.Items) > 1 {
		title = msg.Title()
	}
	for _, it := range msg.Items {
		line := it.Text
		if it.URL != "" {
			line += " " + link("release notes", it.URL)
		}
		body = append(body, line)
	}
	return title, body
}

// truncate keeps s within limit runes, ending with an ellipsis when it was cut.
func truncate(s string, limit int) string {
	r := []rune(s)
	if len(r) <= limit {
		return s
	}
	return string(r[:limit-1]) + "…"
}

// Discord posts to a channel webhook.
type Discord struct{ HTTP *http.Client }

// Send implements Sender.
func (d Discord) Send(ctx context.Context, ch store.Channel, msg Message) error {
	u, _, err := channelURL(ch)
	if err != nil {
		return err
	}
	title, body := lines(msg, func(text, url string) string { return "[" + text + "](<" + url + ">)" })
	text := strings.Join(body, "\n")
	if title != "" {
		text = "**" + title + "**\n" + strings.Join(prefix(body, "• "), "\n")
	}
	payload, _ := json.Marshal(map[string]any{
		"username": "Goliash", "content": truncate(text, 2000),
		"allowed_mentions": map[string]any{"parse": []string{}}, // never ping @everyone from a release name
	})
	return post(ctx, d.HTTP, u, payload, nil)
}

func prefix(items []string, p string) []string {
	out := make([]string, len(items))
	for i, s := range items {
		out[i] = p + s
	}
	return out
}

// Telegram sends through a bot to a chat.
type Telegram struct {
	HTTP *http.Client
	API  string // https://api.telegram.org; tests point it elsewhere
}

// Send implements Sender.
func (t Telegram) Send(ctx context.Context, ch store.Channel, msg Message) error {
	var cfg struct {
		BotToken string `json:"bot_token"`
		ChatID   string `json:"chat_id"`
	}
	if err := json.Unmarshal(ch.Config, &cfg); err != nil || cfg.BotToken == "" || cfg.ChatID == "" {
		return fmt.Errorf("channel %s needs a bot token and a chat id", ch.Name)
	}
	title, body := lines(msg, func(text, u string) string {
		return `<a href="` + html.EscapeString(u) + `">` + text + `</a>`
	})
	for i, b := range body {
		// Escape everything but the link we added.
		text, link, _ := strings.Cut(b, ` <a href="`)
		body[i] = html.EscapeString(text)
		if link != "" {
			body[i] += ` <a href="` + link
		}
	}
	text := strings.Join(body, "\n")
	if title != "" {
		text = "<b>" + html.EscapeString(title) + "</b>\n" + strings.Join(prefix(body, "• "), "\n")
	}
	api := t.API
	if api == "" {
		api = "https://api.telegram.org"
	}
	payload, _ := json.Marshal(map[string]any{
		"chat_id": cfg.ChatID, "text": truncate(text, 4096), "parse_mode": "HTML",
		"link_preview_options": map[string]bool{"is_disabled": true},
	})
	err := post(ctx, t.HTTP, api+"/bot"+url.PathEscape(cfg.BotToken)+"/sendMessage", payload, nil)
	if err != nil {
		// The URL holds the bot token; never let it into logs or the UI.
		return fmt.Errorf("telegram: %s", strings.ReplaceAll(err.Error(), cfg.BotToken, "…"))
	}
	return nil
}

// Ntfy publishes to an ntfy topic (ntfy.sh or self-hosted).
type Ntfy struct{ HTTP *http.Client }

// Send implements Sender.
func (n Ntfy) Send(ctx context.Context, ch store.Channel, msg Message) error {
	var cfg struct {
		URL   string `json:"url"`
		Token string `json:"token"`
	}
	if err := json.Unmarshal(ch.Config, &cfg); err != nil || cfg.URL == "" {
		return fmt.Errorf("channel %s has no url", ch.Name)
	}
	title, body := lines(msg, func(string, string) string { return "" })
	if title == "" {
		title = "Goliash"
	}
	headers := map[string]string{"Title": title, "Tags": "package", "Content-Type": "text/plain; charset=utf-8"}
	if len(msg.Items) == 1 && msg.Items[0].URL != "" {
		headers["Click"] = msg.Items[0].URL
	}
	if cfg.Token != "" {
		headers["Authorization"] = "Bearer " + cfg.Token
	}
	for i := range body {
		body[i] = strings.TrimSpace(body[i])
	}
	return post(ctx, n.HTTP, cfg.URL, []byte(truncate(strings.Join(body, "\n"), 4000)), headers)
}

// Grafana adds every item as an annotation, so deploys show up on dashboards next to
// the graphs they may have changed. Config: Grafana's URL and a service account token
// with the annotations:write permission.
type Grafana struct{ HTTP *http.Client }

// Send implements Sender.
func (g Grafana) Send(ctx context.Context, ch store.Channel, msg Message) error {
	var cfg struct {
		URL   string `json:"url"`
		Token string `json:"token"`
	}
	if err := json.Unmarshal(ch.Config, &cfg); err != nil || cfg.URL == "" || cfg.Token == "" {
		return fmt.Errorf("channel %s needs Grafana's URL and a service account token", ch.Name)
	}
	endpoint := strings.TrimRight(cfg.URL, "/") + "/api/annotations"
	for _, it := range msg.Items {
		tags := []string{"goliash", it.Type}
		for _, t := range []string{it.Service, it.Environment} {
			if t != "" {
				tags = append(tags, t)
			}
		}
		text := it.Text
		if it.URL != "" {
			text += ` <a href="` + html.EscapeString(it.URL) + `">release notes</a>`
		}
		body, _ := json.Marshal(map[string]any{"time": it.At.UnixMilli(), "tags": tags, "text": text})
		if err := post(ctx, g.HTTP, endpoint, body, map[string]string{"Authorization": "Bearer " + cfg.Token}); err != nil {
			return fmt.Errorf("grafana: %w", err)
		}
	}
	return nil
}
