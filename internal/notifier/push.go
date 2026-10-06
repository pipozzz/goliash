// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package notifier

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/pipozzz/goliash/internal/store"
	"github.com/pipozzz/goliash/internal/webpush"
)

// ErrNoBrowsers means no browser is subscribed to a push channel yet.
var ErrNoBrowsers = errors.New("no browser is subscribed to this channel yet: open Notifications in a browser and press Notify this browser")

// WebPush sends to the browsers subscribed to a push channel, signed with the server's
// VAPID key pair (one for the whole server: a browser holds one push subscription,
// which then serves every push channel it was added to).
type WebPush struct {
	Store   *store.Store
	HTTP    *http.Client
	Subject string // the server's public URL, or a mailto: address
}

// PushKeys returns the server's VAPID key pair, made the first time.
func PushKeys(ctx context.Context, st *store.Store) (webpush.Keys, error) {
	raw, err := st.ServerSecret(ctx, "vapid", func() (string, error) {
		k, err := webpush.GenerateKeys()
		if err != nil {
			return "", err
		}
		b, err := json.Marshal(k)
		return string(b), err
	})
	if err != nil {
		return webpush.Keys{}, err
	}
	var k webpush.Keys
	if err := json.Unmarshal([]byte(raw), &k); err != nil || k.Private == "" {
		return webpush.Keys{}, errors.New("the stored web push key pair is damaged")
	}
	return k, nil
}

// PushPayload is what the service worker shows.
type PushPayload struct {
	Title string `json:"title"`
	Body  string `json:"body"`
	URL   string `json:"url,omitempty"`
	Tag   string `json:"tag,omitempty"` // a newer notification with the same tag replaces the older
}

// pushPayload turns a message into a notification: one item as itself, a digest as a
// title and its first lines.
func pushPayload(msg Message) PushPayload {
	if len(msg.Items) == 1 && !msg.Digest {
		it := msg.Items[0]
		k := kindOf(it)
		title := k.Emoji + " " + where(it)
		if where(it) == "" {
			title = k.Emoji + " " + k.Label
		}
		return PushPayload{Title: title, Body: what(it), URL: itemLink(msg.Link, it), Tag: it.Type + ":" + it.Service + ":" + it.Environment}
	}
	var lines []string
	for i, it := range msg.Items {
		if i == 4 {
			lines = append(lines, fmt.Sprintf("…and %d more", len(msg.Items)-4))
			break
		}
		lines = append(lines, kindOf(it).Emoji+" "+it.Text)
	}
	return PushPayload{Title: msg.Title(), Body: strings.Join(lines, "\n"), URL: openLink(msg), Tag: "digest"}
}

// Send implements Sender.
func (p WebPush) Send(ctx context.Context, ch store.Channel, msg Message) error {
	keys, err := PushKeys(ctx, p.Store)
	if err != nil {
		return err
	}
	subs, err := p.Store.ListPushSubscriptions(ctx, ch.Scope, ch.ID)
	if err != nil {
		return err
	}
	if len(subs) == 0 {
		return ErrNoBrowsers
	}
	pl := pushPayload(msg)
	body, _ := json.Marshal(pl)
	for len(body) > webpush.MaxPayload && len(pl.Body) > 0 {
		pl.Body = truncate(pl.Body, len([]rune(pl.Body))/2)
		body, _ = json.Marshal(pl)
	}
	urgent := false
	for _, it := range msg.Items {
		if it.Type == "agent_stale" || it.Note == "eol" {
			urgent = true
		}
	}
	var rep PushReport
	for _, s := range subs {
		var sub webpush.Subscription
		sub.Endpoint = s.Endpoint
		if err := json.Unmarshal([]byte(s.Keys), &sub.Keys); err != nil {
			rep.Failed = append(rep.Failed, s.Label+": "+err.Error())
			continue
		}
		err := webpush.Send(ctx, p.HTTP, keys, p.Subject, sub, webpush.Message{Payload: body, TTL: 24 * time.Hour, Urgent: urgent})
		switch {
		case errors.Is(err, webpush.ErrGone):
			// The browser unsubscribed or the subscription expired.
			_, _ = p.Store.DeletePushSubscription(ctx, ch.Scope, ch.ID, s.Endpoint, "")
			rep.Gone = append(rep.Gone, s.Label)
		case err != nil:
			rep.Failed = append(rep.Failed, s.Label+": "+err.Error())
		default:
			rep.Sent = append(rep.Sent, s.Label)
		}
	}
	if len(rep.Failed) == 0 && len(rep.Gone) == 0 {
		return nil
	}
	return &rep
}

// PushReport says which browsers got a push and which did not, and why, when not all
// did. Deliveries count as sent when one browser got it (retrying would repeat it to
// the others); a test shows the whole report.
type PushReport struct {
	Sent   []string // browsers that got it
	Failed []string // "browser: why"
	Gone   []string // browsers whose subscription had ended; forgotten
}

func (r *PushReport) Error() string {
	var parts []string
	if len(r.Sent) > 0 {
		parts = append(parts, "sent to "+strings.Join(r.Sent, ", "))
	}
	if len(r.Failed) > 0 {
		parts = append(parts, "not delivered to "+strings.Join(r.Failed, "; "))
	}
	if len(r.Gone) > 0 {
		parts = append(parts, strings.Join(r.Gone, ", ")+" had unsubscribed and was removed; press Notify this browser there again")
	}
	return strings.Join(parts, "; ")
}

// Delivered reports whether some browser got the push.
func (r *PushReport) Delivered() bool { return len(r.Sent) > 0 }
