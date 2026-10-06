// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package notifier

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net"
	"net/http"
	"net/smtp"
	"net/textproto"
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
		"teams":    Teams{HTTP: hc},
		"gchat":    GoogleChat{HTTP: hc},
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
	// text is the fallback for notifications and clients without blocks.
	var b strings.Builder
	if msg.Digest || len(msg.Items) > 1 {
		b.WriteString("*" + msg.Title() + "*\n")
		for _, it := range msg.Items {
			b.WriteString("• " + slackLine(it) + "\n")
		}
	} else if len(msg.Items) == 1 {
		b.WriteString(slackLine(msg.Items[0]))
	}
	body, _ := json.Marshal(map[string]any{"text": truncate(strings.TrimSpace(b.String()), 3000), "mrkdwn": true, "blocks": slackBlocks(msg)})
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

// SMTPConfig is a mail relay: the server-wide one, or a channel's own.
type SMTPConfig struct {
	Addr     string // host:port
	Username string
	Password string
	From     string
	TLS      string // "starttls" (when offered; the default), "tls" (implicit, the default on port 465) or "none"
}

// channelSMTP is a channel's own mail server, when it has one, else the server's.
func channelSMTP(server SMTPConfig, raw []byte) SMTPConfig {
	var cfg struct {
		Addr     string `json:"smtp_addr"`
		Username string `json:"smtp_username"`
		Password string `json:"smtp_password"`
		From     string `json:"smtp_from"`
		TLS      string `json:"smtp_tls"`
	}
	if json.Unmarshal(raw, &cfg) != nil || cfg.Addr == "" {
		return server
	}
	return SMTPConfig{Addr: cfg.Addr, Username: cfg.Username, Password: cfg.Password, From: cfg.From, TLS: cfg.TLS}
}

// Email sends mail through SMTP (STARTTLS when the server offers it): HTML with a
// plain-text alternative.
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
	if u := openLink(msg); u != "" {
		fmt.Fprintf(&body, "\r\nOpen in Goliash: %s\r\n", u)
	}
	body.WriteString("\r\n-- \r\nGoliash\r\n")
	page, err := emailHTML(msg)
	if err != nil {
		return err
	}
	return e.send(ctx, ch, msg.Title(), body.String(), page)
}

// SendPlain sends a plain-text e-mail to the channel's recipients.
func (e Email) SendPlain(ctx context.Context, ch store.Channel, subject, text string) error {
	return e.send(ctx, ch, subject, text, "")
}

// send mails text, and page as its HTML alternative when given.
func (e Email) send(ctx context.Context, ch store.Channel, subject, text, page string) error {
	conf := channelSMTP(e.Config, ch.Config)
	if conf.Addr == "" || conf.From == "" {
		return ErrNoSMTP
	}
	var cfg struct {
		To []string `json:"to"`
	}
	if err := json.Unmarshal(ch.Config, &cfg); err != nil || len(cfg.To) == 0 {
		return fmt.Errorf("channel %s has no recipients", ch.Name)
	}
	for _, addr := range append([]string{conf.From}, cfg.To...) {
		if strings.ContainsAny(addr, "\r\n") {
			return errors.New("invalid e-mail address")
		}
	}
	var body strings.Builder
	fmt.Fprintf(&body, "From: %s\r\nTo: %s\r\nSubject: %s\r\nDate: %s\r\nMIME-Version: 1.0\r\n",
		conf.From, strings.Join(cfg.To, ", "), mime.QEncoding.Encode("utf-8", strings.NewReplacer("\r", " ", "\n", " ").Replace(subject)),
		time.Now().Format(time.RFC1123Z))
	if page == "" {
		body.WriteString("Content-Type: text/plain; charset=utf-8\r\nContent-Transfer-Encoding: quoted-printable\r\n\r\n")
		body.WriteString(quoted(text))
	} else {
		mw := multipart.NewWriter(&body)
		fmt.Fprintf(&body, "Content-Type: multipart/alternative; boundary=%s\r\n\r\n", mw.Boundary())
		for _, part := range []struct{ typ, content string }{{"text/plain", text}, {"text/html", page}} {
			w, err := mw.CreatePart(textproto.MIMEHeader{
				"Content-Type":              {part.typ + "; charset=utf-8"},
				"Content-Transfer-Encoding": {"quoted-printable"},
			})
			if err != nil {
				return err
			}
			_, _ = io.WriteString(w, quoted(part.content))
		}
		if err := mw.Close(); err != nil {
			return err
		}
	}

	return sendMail(ctx, conf, cfg.To, []byte(body.String()))
}

// sendMail delivers one message: implicit TLS, STARTTLS when the server offers it, or
// neither; authenticated only over TLS (or to localhost), within the context's deadline.
func sendMail(ctx context.Context, conf SMTPConfig, to []string, msg []byte) error {
	host, port, err := net.SplitHostPort(conf.Addr)
	if err != nil {
		return fmt.Errorf("mail server %q: give host:port", conf.Addr)
	}
	mode := conf.TLS
	if mode == "" {
		mode = "starttls"
		if port == "465" {
			mode = "tls"
		}
	}
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
	}
	tlsConf := &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}
	var conn net.Conn
	if mode == "tls" {
		conn, err = (&tls.Dialer{Config: tlsConf}).DialContext(ctx, "tcp", conf.Addr)
	} else {
		conn, err = (&net.Dialer{}).DialContext(ctx, "tcp", conf.Addr) //nolint:gosec // the admin's own mail server
	}
	if err != nil {
		return err
	}
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	}
	c, err := smtp.NewClient(conn, host)
	if err != nil {
		_ = conn.Close()
		return err
	}
	defer func() { _ = c.Close() }()
	if mode == "starttls" {
		if ok, _ := c.Extension("STARTTLS"); ok {
			if err := c.StartTLS(tlsConf); err != nil {
				return err
			}
		}
	}
	if conf.Username != "" {
		// PlainAuth refuses to send the password unencrypted, except to localhost.
		if err := c.Auth(smtp.PlainAuth("", conf.Username, conf.Password, host)); err != nil {
			return err
		}
	}
	if err := c.Mail(conf.From); err != nil {
		return err
	}
	for _, addr := range to {
		if err := c.Rcpt(addr); err != nil {
			return err
		}
	}
	w, err := c.Data()
	if err != nil {
		return err
	}
	if _, err := w.Write(msg); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return c.Quit()
}

// quoted encodes s as quoted-printable, which keeps lines short and non-ASCII safe.
func quoted(s string) string {
	var b strings.Builder
	w := quotedprintable.NewWriter(&b)
	_, _ = io.WriteString(w, s)
	_ = w.Close()
	return b.String()
}
