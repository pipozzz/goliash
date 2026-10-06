// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

// Package webpush sends Web Push messages: the payload encrypted for the browser
// (RFC 8291, aes128gcm from RFC 8188) and the request signed with VAPID (RFC 8292),
// with the standard library only.
package webpush

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

var b64 = base64.RawURLEncoding

// decode reads base64url with or without padding, as browsers and libraries vary.
func decode(s string) ([]byte, error) {
	return b64.DecodeString(strings.TrimRight(s, "="))
}

// Keys is a VAPID key pair: the server identifies itself to push services with it, and
// browsers subscribe with its public half (the applicationServerKey).
type Keys struct {
	Private string `json:"vapid_private"` // base64url, the 32-byte scalar
	Public  string `json:"vapid_public"`  // base64url, the 65-byte uncompressed point
}

// GenerateKeys makes a new VAPID key pair.
func GenerateKeys() (Keys, error) {
	k, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		return Keys{}, err
	}
	return Keys{Private: b64.EncodeToString(k.Bytes()), Public: b64.EncodeToString(k.PublicKey().Bytes())}, nil
}

// signer returns the ECDSA key for signing VAPID tokens.
func (k Keys) signer() (*ecdsa.PrivateKey, error) {
	d, err := decode(k.Private)
	if err != nil {
		return nil, errors.New("webpush: bad VAPID private key")
	}
	priv, err := ecdsa.ParseRawPrivateKey(elliptic.P256(), d)
	if err != nil {
		return nil, fmt.Errorf("webpush: bad VAPID private key: %w", err)
	}
	return priv, nil
}

// Subscription is what a browser's PushManager.subscribe() returns.
type Subscription struct {
	Endpoint string `json:"endpoint"`
	Keys     struct {
		P256dh string `json:"p256dh"`
		Auth   string `json:"auth"`
	} `json:"keys"`
}

// Check validates a subscription before it is stored: an https endpoint on a known push
// service (the server will POST there, so not anywhere), and keys of the right sizes.
func (s Subscription) Check() error {
	u, err := url.Parse(s.Endpoint)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
		return errors.New("webpush: the endpoint is not an https URL")
	}
	if !PushService(u.Hostname()) {
		return fmt.Errorf("webpush: %s is not a known push service", u.Hostname())
	}
	if p, err := decode(s.Keys.P256dh); err != nil || len(p) != 65 {
		return errors.New("webpush: bad p256dh key")
	}
	if a, err := decode(s.Keys.Auth); err != nil || len(a) != 16 {
		return errors.New("webpush: bad auth secret")
	}
	return nil
}

// pushHosts are the push services of the browsers people use: Chrome, Edge and other
// Chromium browsers (FCM), Firefox (Mozilla), Safari (Apple) and Windows (WNS).
// Chrome's endpoints are on fcm.googleapis.com and, lately, other google.com hosts.
var pushHosts = []string{"fcm.googleapis.com", "android.googleapis.com", "updates.push.services.mozilla.com"}

var pushSuffixes = []string{".googleapis.com", ".google.com", ".push.services.mozilla.com", ".push.apple.com", ".notify.windows.com", ".wns.windows.com"}

// PushService reports whether host belongs to a known push service.
func PushService(host string) bool {
	host = strings.ToLower(host)
	for _, h := range pushHosts {
		if host == h {
			return true
		}
	}
	for _, s := range pushSuffixes {
		if strings.HasSuffix(host, s) {
			return true
		}
	}
	return false
}

// recordSize is the record size written in the header; one record holds the payload.
const recordSize = 4096

// MaxPayload is the largest plaintext one message carries (4096 minus the header,
// the delimiter and the tag, rounded down for the push services' own limits).
const MaxPayload = 3800

// Encrypt encrypts plaintext for a subscription (RFC 8291).
func Encrypt(sub Subscription, plaintext []byte) ([]byte, error) {
	salt := make([]byte, 16)
	_, _ = rand.Read(salt)
	as, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	return encrypt(sub, plaintext, salt, as)
}

func encrypt(sub Subscription, plaintext, salt []byte, as *ecdh.PrivateKey) ([]byte, error) {
	if len(plaintext) > MaxPayload {
		return nil, fmt.Errorf("webpush: payload of %d bytes is over %d", len(plaintext), MaxPayload)
	}
	uaRaw, err := decode(sub.Keys.P256dh)
	if err != nil {
		return nil, err
	}
	ua, err := ecdh.P256().NewPublicKey(uaRaw)
	if err != nil {
		return nil, fmt.Errorf("webpush: bad p256dh key: %w", err)
	}
	auth, err := decode(sub.Keys.Auth)
	if err != nil {
		return nil, err
	}
	shared, err := as.ECDH(ua)
	if err != nil {
		return nil, err
	}
	asPub := as.PublicKey().Bytes()
	// IKM = HKDF(auth, ecdh_secret, "WebPush: info" || 0x00 || ua_public || as_public, 32)
	info := append(append([]byte("WebPush: info\x00"), uaRaw...), asPub...)
	ikm, err := hkdf.Key(sha256.New, shared, auth, string(info), 32)
	if err != nil {
		return nil, err
	}
	prk, err := hkdf.Extract(sha256.New, ikm, salt)
	if err != nil {
		return nil, err
	}
	cek, err := hkdf.Expand(sha256.New, prk, "Content-Encoding: aes128gcm\x00", 16)
	if err != nil {
		return nil, err
	}
	nonce, err := hkdf.Expand(sha256.New, prk, "Content-Encoding: nonce\x00", 12)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(cek)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	// Header: salt, record size, key id length and the server's public key.
	var out bytes.Buffer
	out.Write(salt)
	_ = binary.Write(&out, binary.BigEndian, uint32(recordSize))
	out.WriteByte(65) // the key id: the uncompressed P-256 point, always 65 bytes
	out.Write(asPub)
	padded := append(append([]byte{}, plaintext...), 0x02) // the last (and only) record
	out.Write(gcm.Seal(nil, nonce, padded, nil))
	return out.Bytes(), nil
}

// vapid signs a token for the endpoint's push service (RFC 8292).
func vapid(keys Keys, endpoint, subject string, now time.Time) (string, error) {
	priv, err := keys.signer()
	if err != nil {
		return "", err
	}
	u, err := url.Parse(endpoint)
	if err != nil {
		return "", err
	}
	header := b64.EncodeToString([]byte(`{"typ":"JWT","alg":"ES256"}`))
	claims, _ := json.Marshal(map[string]any{"aud": u.Scheme + "://" + u.Host, "exp": now.Add(12 * time.Hour).Unix(), "sub": subject})
	signing := header + "." + b64.EncodeToString(claims)
	digest := sha256.Sum256([]byte(signing))
	r, s, err := ecdsa.Sign(rand.Reader, priv, digest[:])
	if err != nil {
		return "", err
	}
	sig := make([]byte, 64)
	r.FillBytes(sig[:32])
	s.FillBytes(sig[32:])
	return "vapid t=" + signing + "." + b64.EncodeToString(sig) + ", k=" + keys.Public, nil
}

// ErrGone means the subscription expired or was revoked; forget it.
var ErrGone = errors.New("webpush: subscription is gone")

// Message is one push.
type Message struct {
	Payload []byte
	TTL     time.Duration // how long the push service keeps it for an offline browser
	Urgent  bool
	Topic   string // a newer message with the same topic replaces an undelivered one
}

// Send delivers a message. subject identifies the sender to the push service: a
// mailto: address or an https URL.
func Send(ctx context.Context, hc *http.Client, keys Keys, subject string, sub Subscription, m Message) error {
	if err := sub.Check(); err != nil {
		return err
	}
	body, err := Encrypt(sub, m.Payload)
	if err != nil {
		return err
	}
	auth, err := vapid(keys, sub.Endpoint, subject, time.Now())
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, sub.Endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	ttl := m.TTL
	if ttl <= 0 {
		ttl = 24 * time.Hour
	}
	req.Header.Set("Authorization", auth)
	req.Header.Set("Content-Encoding", "aes128gcm")
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("TTL", strconv.Itoa(int(ttl.Seconds())))
	if m.Urgent {
		req.Header.Set("Urgency", "high")
	}
	if m.Topic != "" {
		req.Header.Set("Topic", m.Topic)
	}
	resp, err := hc.Do(req) //nolint:gosec // Check allows known push services only
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	switch {
	case resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone:
		return ErrGone
	case resp.StatusCode >= 300:
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 200))
		return fmt.Errorf("webpush: %s answered %d: %s", req.URL.Host, resp.StatusCode, strings.TrimSpace(string(b)))
	}
	return nil
}
