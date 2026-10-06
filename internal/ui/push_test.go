// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package ui

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/pipozzz/goliash/internal/store"
)

func postJSON(t *testing.T, c *http.Client, u, body string) (int, string) {
	t.Helper()
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, u, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func TestWebPushChannel(t *testing.T) {
	e := newUIEnv(t)
	ctx := context.Background()
	admin := e.as(store.RoleOwner)
	if _, body, _ := post(t, admin, e.srv.URL+"/notifications/channels", url.Values{"name": {"phones"}, "type": {"push"}}); !strings.Contains(body, "Channel phones added") {
		t.Fatalf("add push channel: %s", body)
	}
	chans, _ := e.st.ListChannels(ctx, e.ws.Scope())
	ch := chans[0]
	_, page := get(t, admin, e.srv.URL+"/notifications", nil)
	if !strings.Contains(page, `data-push-key="B`) || !strings.Contains(page, "Notify this browser") || !strings.Contains(page, "0 browsers") {
		t.Fatal("notifications page misses the push controls or the key")
	}

	// A viewer adds their own browser; only known push services are accepted.
	viewer := e.as(store.RoleViewer)
	ua, _ := ecdh.P256().GenerateKey(rand.Reader)
	keys := `"keys":{"p256dh":"` + base64.RawURLEncoding.EncodeToString(ua.PublicKey().Bytes()) + `","auth":"` + base64.RawURLEncoding.EncodeToString(make([]byte, 16)) + `"}`
	if code, _ := postJSON(t, viewer, e.srv.URL+"/notifications/channels/"+ch.ID+"/push", `{"endpoint":"https://169.254.169.254/x",`+keys+`}`); code != http.StatusBadRequest {
		t.Fatalf("a non-push endpoint was accepted: %d", code)
	}
	endpoint := "https://fcm.googleapis.com/fcm/send/abc"
	if code, body := postJSON(t, viewer, e.srv.URL+"/notifications/channels/"+ch.ID+"/push", `{"endpoint":"`+endpoint+`",`+keys+`}`); code != http.StatusOK || !strings.Contains(body, "now gets the notifications of phones") {
		t.Fatalf("subscribe: %d %s", code, body)
	}
	if _, body := postJSON(t, viewer, e.srv.URL+"/ui/push/status", `{"endpoint":"`+endpoint+`"}`); !strings.Contains(body, ch.ID) {
		t.Fatalf("status: %s", body)
	}
	// Another person cannot see or remove that browser.
	if _, body := postJSON(t, admin, e.srv.URL+"/ui/push/status", `{"endpoint":"`+endpoint+`"}`); strings.Contains(body, ch.ID) {
		t.Fatal("someone else's browser reported as subscribed")
	}
	if _, page = get(t, admin, e.srv.URL+"/notifications", nil); !strings.Contains(page, "1 browser") {
		t.Fatal("count of browsers missing")
	}
	if code, _ := postJSON(t, viewer, e.srv.URL+"/notifications/channels/"+ch.ID+"/push/delete", `{"endpoint":"`+endpoint+`"}`); code != http.StatusOK {
		t.Fatal("unsubscribe failed")
	}
	if subs, _ := e.st.ListPushSubscriptions(ctx, e.ws.Scope(), ch.ID); len(subs) != 0 {
		t.Fatal("subscription kept")
	}

	// The service worker is served from the root.
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, e.srv.URL+"/sw.js", nil)
	resp, err := viewer.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Service-Worker-Allowed") != "/" || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/javascript") {
		t.Fatalf("sw.js: %d %v", resp.StatusCode, resp.Header)
	}
	if code, body := get(t, http.DefaultClient, e.srv.URL+"/manifest.webmanifest", nil); code != http.StatusOK || !strings.Contains(body, `"display":"standalone"`) {
		t.Fatalf("manifest: %d %s", code, body)
	}
}
