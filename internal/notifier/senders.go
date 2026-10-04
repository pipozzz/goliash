// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package notifier

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/smtp"
	"strconv"
	"strings"
	"time"

	"github.com/pipozzz/goliash/internal/store"
)

// DefaultSenders returns senders for every channel type. smtp may be zero, in which
// case e-mail channels fail with ErrNoSMTP.
func DefaultSenders(hc *http.Client, smtpCfg SMTPConfig) map[string]Sender {
	return map[string]Sender{
		"slack":    Slack{HTTP: hc},
		"webhook":  Webhook{HTTP: hc},
		"email":    Email{Config: smtpCfg},
		"discord":  Discord{HTTP: hc},
		"telegram": Telegram{HTTP: hc},
		"ntfy":     Ntfy{HTTP: hc},
		"grafana":  Grafana{HTTP: hc},
	}
}

func post(ctx context.Context, hc *http.Client, url string, body []byte, headers map[string]string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "goliash")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 200))
		return fmt.Errorf("answered %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	return nil
}

func channelURL(ch store.Channel) (string, string, error) {
	var cfg struct {
		URL    string `json:"url"`
		Secret string `json:"secret"`
	}
	if err := json.Unmarshal(ch.Config, &cfg); err != nil || cfg.URL == "" {
		return "", "", fmt.Errorf("channel %s has no url", ch.Name)
	}
	return cfg.URL, cfg.Secret, nil
}

// Slack posts to an incoming webhook.
type Slack struct{ HTTP *http.Client }

// Send implements Sender.
func (s Slack) Send(ctx context.Context, ch store.Channel, msg Message) error {
	url, _, err := channelURL(ch)
	if err != nil {
		return err
	}
	var b strings.Builder
	if msg.Digest || len(msg.Items) > 1 {
		b.WriteString("*" + msg.Title() + "*\n")
		for _, it := range msg.Items {
			b.WriteString("• " + slackLine(it) + "\n")
		}
	} else if len(msg.Items) == 1 {
		b.WriteString(slackLine(msg.Items[0]))
	}
	body, _ := json.Marshal(map[string]any{"text": strings.TrimSpace(b.String()), "mrkdwn": true})
	return post(ctx, s.HTTP, url, body, nil)
}

func slackLine(it Item) string {
	if it.URL != "" {
		return it.Text + " <" + it.URL + "|release notes>"
	}
	return it.Text
}

// Webhook posts the message as JSON. With a secret, the body is signed:
// X-Goliash-Signature: sha256=hex(HMAC-SHA256(secret, timestamp + "." + body)).
type Webhook struct{ HTTP *http.Client }

// Send implements Sender.
func (w Webhook) Send(ctx context.Context, ch store.Channel, msg Message) error {
	url, secret, err := channelURL(ch)
	if err != nil {
		return err
	}
	body, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	headers := map[string]string{"X-Goliash-Timestamp": ts}
	if secret != "" {
		headers["X-Goliash-Signature"] = "sha256=" + Sign(secret, ts, body)
	}
	return post(ctx, w.HTTP, url, body, headers)
}

// Sign returns the hex HMAC-SHA256 a webhook receiver checks.
func Sign(secret, timestamp string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(timestamp + "."))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

// SMTPConfig is the server-wide mail relay.
type SMTPConfig struct {
	Addr     string // host:port
	Username string
	Password string
	From     string
}

// Email sends plain-text mail through SMTP (STARTTLS when the server offers it).
type Email struct{ Config SMTPConfig }

// Send implements Sender.
func (e Email) Send(ctx context.Context, ch store.Channel, msg Message) error {
	var body strings.Builder
	for _, it := range msg.Items {
		fmt.Fprintf(&body, "- %s (%s)\r\n", it.Text, it.At.Format("2006-01-02 15:04 MST"))
		if it.URL != "" {
			fmt.Fprintf(&body, "  %s\r\n", it.URL)
		}
	}
	body.WriteString("\r\n-- \r\nGoliash\r\n")
	return e.SendPlain(ctx, ch, msg.Title(), body.String())
}

// SendPlain sends a plain-text e-mail to the channel's recipients.
func (e Email) SendPlain(_ context.Context, ch store.Channel, subject, text string) error {
	if e.Config.Addr == "" || e.Config.From == "" {
		return ErrNoSMTP
	}
	var cfg struct {
		To []string `json:"to"`
	}
	if err := json.Unmarshal(ch.Config, &cfg); err != nil || len(cfg.To) == 0 {
		return fmt.Errorf("channel %s has no recipients", ch.Name)
	}
	for _, addr := range append([]string{e.Config.From}, cfg.To...) {
		if strings.ContainsAny(addr, "\r\n") {
			return errors.New("invalid e-mail address")
		}
	}
	var body strings.Builder
	fmt.Fprintf(&body, "From: %s\r\nTo: %s\r\nSubject: %s\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n",
		e.Config.From, strings.Join(cfg.To, ", "), strings.NewReplacer("\r", " ", "\n", " ").Replace(subject))
	body.WriteString(text)

	var auth smtp.Auth
	if e.Config.Username != "" {
		host, _, _ := strings.Cut(e.Config.Addr, ":")
		auth = smtp.PlainAuth("", e.Config.Username, e.Config.Password, host)
	}
	return smtp.SendMail(e.Config.Addr, auth, e.Config.From, cfg.To, []byte(body.String()))
}
