// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package notifier

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pipozzz/goliash/internal/store"
)

// fakePush stands in for FCM: requests to fcm.googleapis.com reach it.
func fakePush(t *testing.T, status *int) (*http.Client, func() int) {
	t.Helper()
	var mu sync.Mutex
	n := 0
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.Header.Get("Content-Encoding") != "aes128gcm" || r.Header.Get("Authorization") == "" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if strings.Contains(r.URL.Path, "refused") { // as Apple answers a token it does not accept
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"reason":"BadJwtToken"}`))
			return
		}
		n++
		w.WriteHeader(*status)
	}))
	t.Cleanup(srv.Close)
	hc := srv.Client()
	tr := hc.Transport.(*http.Transport)
	tr.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, srv.Listener.Addr().String())
	}
	tr.TLSClientConfig.InsecureSkipVerify = true //nolint:gosec // test server
	return hc, func() int { mu.Lock(); defer mu.Unlock(); return n }
}

func browserKeys(t *testing.T) string {
	t.Helper()
	ua, _ := ecdh.P256().GenerateKey(rand.Reader)
	auth := make([]byte, 16)
	_, _ = rand.Read(auth)
	b, _ := json.Marshal(map[string]string{
		"p256dh": base64.RawURLEncoding.EncodeToString(ua.PublicKey().Bytes()),
		"auth":   base64.RawURLEncoding.EncodeToString(auth),
	})
	return string(b)
}

func TestWebPush(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	status := http.StatusCreated
	hc, count := fakePush(t, &status)
	e.n.senders["push"] = WebPush{Store: e.st, HTTP: hc, Subject: "https://goliash.example.com"}
	ch := e.channel("push", "phones", nil)
	msg := Message{Workspace: "Default", Items: []Item{{Type: "test", Text: "hello", At: time.Now()}}}

	if err := e.n.senders["push"].Send(ctx, ch, msg); !errors.Is(err, ErrNoBrowsers) {
		t.Fatalf("no browsers: %v", err)
	}
	u, err := e.st.CreateUser(ctx, e.sc.OrgID, "ops@example.com", "", "member")
	if err != nil {
		t.Fatal(err)
	}
	for _, ep := range []string{"https://fcm.googleapis.com/fcm/send/a", "https://fcm.googleapis.com/fcm/send/b"} {
		if _, err := e.st.SavePushSubscription(ctx, e.sc, store.PushSubscription{ChannelID: ch.ID, UserID: u.ID, Endpoint: ep, Keys: browserKeys(t), Label: "Chrome on macOS"}); err != nil {
			t.Fatal(err)
		}
	}
	// A rule's instant message reaches both browsers.
	e.rule(ch, "instant", []string{"new_release"}, Filter{})
	e.emit(release(e.svc, "1.5.0", "1.6.0", "minor"))
	if count() != 2 {
		t.Fatalf("pushed %d, want 2", count())
	}
	// The push service says the subscriptions are gone: they are forgotten.
	status = http.StatusGone
	var report *PushReport
	if err := e.n.senders["push"].Send(ctx, ch, msg); !errors.As(err, &report) || len(report.Gone) != 2 || report.Delivered() {
		t.Fatalf("gone: %v", err)
	}
	if subs, _ := e.st.ListPushSubscriptions(ctx, e.sc, ch.ID); len(subs) != 0 {
		t.Fatalf("gone subscriptions kept: %d", len(subs))
	}
	// One browser gets it, another is refused: the report names both and why; the
	// delivery counts as sent, so the first does not get it again.
	status = http.StatusCreated
	for _, ep := range []string{"https://fcm.googleapis.com/fcm/send/ok", "https://web.push.apple.com/refused"} {
		label := "Chrome on macOS"
		if strings.Contains(ep, "apple") {
			label = "Safari on iOS"
		}
		if _, err := e.st.SavePushSubscription(ctx, e.sc, store.PushSubscription{ChannelID: ch.ID, UserID: u.ID, Endpoint: ep, Keys: browserKeys(t), Label: label}); err != nil {
			t.Fatal(err)
		}
	}
	err = e.n.senders["push"].Send(ctx, ch, msg)
	if !errors.As(err, &report) || !report.Delivered() || !strings.Contains(err.Error(), "sent to Chrome on macOS") ||
		!strings.Contains(err.Error(), "not delivered to Safari on iOS: webpush: web.push.apple.com answered 403") || !strings.Contains(err.Error(), "BadJwtToken") {
		t.Fatalf("mixed report: %v", err)
	}
	before := count()
	e.emit(release(e.svc, "1.6.0", "1.7.0", "minor"))
	_ = e.n.DeliverDue(ctx)
	if count() != before+1 {
		t.Fatalf("pushed %d more, want 1 (no retry for the browser that got it)", count()-before)
	}
	// The key pair is made once.
	k1, _ := PushKeys(ctx, e.st)
	k2, _ := PushKeys(ctx, e.st)
	if k1.Public == "" || k1 != k2 {
		t.Fatal("VAPID keys differ between calls")
	}
}

func TestPushPayload(t *testing.T) {
	m := richMessage()
	p := pushPayload(m)
	if p.Title != m.Title() || p.URL != "https://goliash.example.com/" || p.Tag != "digest" {
		t.Fatalf("digest payload %+v", p)
	}
	m.Digest, m.Items = false, m.Items[1:2]
	if p = pushPayload(m); p.Title != "⛔ postgres · cefiro · prod" || p.URL != "https://goliash.example.com/?q=postgres" {
		t.Fatalf("single payload %+v", p)
	}
}
