// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package webpush

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hkdf"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func mustDecode(t *testing.T, s string) []byte {
	t.Helper()
	b, err := decode(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// The example of RFC 8291, appendix A.
const (
	rfcPlaintext = "When I grow up, I want to be a watermelon"
	rfcASPrivate = "yfWPiYE-n46HLnH0KqZOF1fJJU3MYrct3AELtAQ-oRw"
	rfcUAPrivate = "q1dXpw3UpT5VOmu_cf_v6ih07Aems3njxI-JWgLcM94"
	rfcUAPublic  = "BCVxsr7N_eNgVRqvHtD0zTZsEc6-VV-JvLexhqUzORcxaOzi6-AYWXvTBHm4bjyPjs7Vd8pZGH6SRpkNtoIAiw4"
	rfcAuth      = "BTBZMqHH6r4Tts7J_aSIgg"
	rfcSalt      = "DGv6ra1nlYgDCS1FRnbzlw"
	rfcBody      = "DGv6ra1nlYgDCS1FRnbzlwAAEABBBP4z9KsN6nGRTbVYI_c7VJSPQTBtkgcy27mlmlMoZIIgDll6e3vCYLocInmYWAmS6TlzAC8wEqKK6PBru3jl7A_yl95bQpu6cVPTpK4Mqgkf1CXztLVBSt2Ks3oZwbuwXPXLWyouBWLVWGNWQexSgSxsj_Qulcy4a-fN"
)

func rfcSubscription() Subscription {
	var s Subscription
	s.Endpoint = "https://fcm.googleapis.com/fcm/send/x"
	s.Keys.P256dh, s.Keys.Auth = rfcUAPublic, rfcAuth
	return s
}

func TestEncryptRFC8291(t *testing.T) {
	as, err := ecdh.P256().NewPrivateKey(mustDecode(t, rfcASPrivate))
	if err != nil {
		t.Fatal(err)
	}
	got, err := encrypt(rfcSubscription(), []byte(rfcPlaintext), mustDecode(t, rfcSalt), as)
	if err != nil {
		t.Fatal(err)
	}
	if b64.EncodeToString(got) != rfcBody {
		t.Fatalf("body\n got %s\nwant %s", b64.EncodeToString(got), rfcBody)
	}
}

// decrypt is the browser's side, to check a round trip with fresh keys.
func decrypt(t *testing.T, ua *ecdh.PrivateKey, auth, body []byte) string {
	t.Helper()
	salt, rs, idlen := body[:16], binary.BigEndian.Uint32(body[16:20]), int(body[20])
	asRaw, ct := body[21:21+idlen], body[21+idlen:]
	if rs != recordSize {
		t.Fatalf("record size %d", rs)
	}
	as, err := ecdh.P256().NewPublicKey(asRaw)
	if err != nil {
		t.Fatal(err)
	}
	shared, _ := ua.ECDH(as)
	info := append(append([]byte("WebPush: info\x00"), ua.PublicKey().Bytes()...), asRaw...)
	ikm, _ := hkdf.Key(sha256.New, shared, auth, string(info), 32)
	prk, _ := hkdf.Extract(sha256.New, ikm, salt)
	cek, _ := hkdf.Expand(sha256.New, prk, "Content-Encoding: aes128gcm\x00", 16)
	nonce, _ := hkdf.Expand(sha256.New, prk, "Content-Encoding: nonce\x00", 12)
	block, _ := aes.NewCipher(cek)
	gcm, _ := cipher.NewGCM(block)
	plain, err := gcm.Open(nil, nonce, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	if plain[len(plain)-1] != 0x02 {
		t.Fatal("no last-record delimiter")
	}
	return string(plain[:len(plain)-1])
}

func TestSendRoundTrip(t *testing.T) {
	ua, _ := ecdh.P256().GenerateKey(nil)
	auth := []byte("0123456789abcdef")
	keys, err := GenerateKeys()
	if err != nil {
		t.Fatal(err)
	}
	var got *http.Request
	var body []byte
	status := http.StatusCreated
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r
		body = make([]byte, r.ContentLength)
		_, _ = r.Body.Read(body)
		w.WriteHeader(status)
	}))
	defer srv.Close()
	// The test server is not a push service; route fcm.googleapis.com to it.
	hc := srv.Client()
	hc.Transport.(*http.Transport).DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, srv.Listener.Addr().String())
	}
	hc.Transport.(*http.Transport).TLSClientConfig.InsecureSkipVerify = true //nolint:gosec // test server
	var sub Subscription
	sub.Endpoint = "https://fcm.googleapis.com/fcm/send/abc"
	sub.Keys.P256dh, sub.Keys.Auth = b64.EncodeToString(ua.PublicKey().Bytes()), b64.EncodeToString(auth)
	if err := Send(context.Background(), hc, keys, "https://goliash.example.com", sub, Message{Payload: []byte(`{"title":"hi"}`), Urgent: true, Topic: "t1"}); err != nil {
		t.Fatal(err)
	}
	if decrypt(t, ua, auth, body) != `{"title":"hi"}` {
		t.Fatal("round trip")
	}
	if got.Header.Get("Content-Encoding") != "aes128gcm" || got.Header.Get("Urgency") != "high" || got.Header.Get("TTL") != "86400" || got.Header.Get("Topic") != "t1" {
		t.Fatalf("headers %v", got.Header)
	}
	checkVAPID(t, got.Header.Get("Authorization"), keys)

	status = http.StatusGone
	if err := Send(context.Background(), hc, keys, "https://goliash.example.com", sub, Message{Payload: []byte("x")}); !errors.Is(err, ErrGone) {
		t.Fatalf("gone: %v", err)
	}
}

// checkVAPID verifies the token's signature with the public key, and its claims.
func checkVAPID(t *testing.T, header string, keys Keys) {
	t.Helper()
	tok, k, ok := strings.Cut(strings.TrimPrefix(header, "vapid t="), ", k=")
	if !ok || k != keys.Public {
		t.Fatalf("authorization %q", header)
	}
	parts := strings.Split(tok, ".")
	pub, err := ecdsa.ParseUncompressedPublicKey(elliptic.P256(), mustDecode(t, keys.Public))
	if err != nil {
		t.Fatal(err)
	}
	sig := mustDecode(t, parts[2])
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if !ecdsa.Verify(pub, digest[:], new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:])) {
		t.Fatal("VAPID signature does not verify")
	}
	var claims struct {
		Aud string `json:"aud"`
		Exp int64  `json:"exp"`
		Sub string `json:"sub"`
	}
	_ = json.Unmarshal(mustDecode(t, parts[1]), &claims)
	if claims.Aud != "https://fcm.googleapis.com" || claims.Sub != "https://goliash.example.com" || claims.Exp < time.Now().Unix() {
		t.Fatalf("claims %+v", claims)
	}
}

func TestCheck(t *testing.T) {
	sub := rfcSubscription()
	if err := sub.Check(); err != nil {
		t.Fatal(err)
	}
	for _, endpoint := range []string{"http://fcm.googleapis.com/x", "https://169.254.169.254/latest", "https://evil.example.com/push", "https://fcm.googleapis.com.evil.com/x", "https://google.com.evil.com/x"} {
		sub.Endpoint = endpoint
		if sub.Check() == nil {
			t.Errorf("%s accepted", endpoint)
		}
	}
	for _, host := range []string{"jmt17.google.com", "web.push.apple.com", "wns2-par02p.notify.windows.com", "updates.push.services.mozilla.com"} {
		if !PushService(host) {
			t.Errorf("%s refused", host)
		}
	}
}
