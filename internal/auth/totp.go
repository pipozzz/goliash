// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1" //nolint:gosec // RFC 6238 TOTP uses HMAC-SHA1, as every authenticator app does
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// TOTP (RFC 6238): 6 digits, 30-second steps, HMAC-SHA1, what authenticator apps
// (Google Authenticator, 1Password, Authy, Bitwarden) expect.
const (
	totpStep   = 30
	totpDigits = 6
)

var b32 = base32.StdEncoding.WithPadding(base32.NoPadding)

// NewTOTPSecret returns a random 160-bit secret in base32, as apps take it.
func NewTOTPSecret() string {
	b := make([]byte, 20)
	_, _ = rand.Read(b)
	return b32.EncodeToString(b)
}

// TOTPURI is the otpauth:// URI an app scans from a QR code.
func TOTPURI(secret, issuer, account string) string {
	q := url.Values{"secret": {secret}, "issuer": {issuer}, "algorithm": {"SHA1"}, "digits": {"6"}, "period": {"30"}}
	return "otpauth://totp/" + url.PathEscape(issuer+":"+account) + "?" + q.Encode()
}

// TOTPCode is the code for a secret at a time.
func TOTPCode(secret string, at time.Time) (string, error) {
	key, err := b32.DecodeString(strings.ToUpper(strings.TrimSpace(secret)))
	if err != nil {
		return "", fmt.Errorf("totp secret: %w", err)
	}
	return hotp(key, uint64(at.Unix()/totpStep)), nil //nolint:gosec // time after 1970
}

func hotp(key []byte, counter uint64) string {
	var msg [8]byte
	binary.BigEndian.PutUint64(msg[:], counter)
	mac := hmac.New(sha1.New, key)
	_, _ = mac.Write(msg[:])
	sum := mac.Sum(nil)
	off := sum[len(sum)-1] & 0x0f
	n := binary.BigEndian.Uint32(sum[off:off+4]) & 0x7fffffff
	return fmt.Sprintf("%0*d", totpDigits, n%1_000_000)
}

// VerifyTOTP checks a code against the secret, allowing one step of clock drift either
// way. It returns the step that matched, so a caller can refuse reusing it.
func VerifyTOTP(secret, code string, at time.Time) (step int64, ok bool) {
	code = strings.ReplaceAll(strings.TrimSpace(code), " ", "")
	if len(code) != totpDigits {
		return 0, false
	}
	key, err := b32.DecodeString(strings.ToUpper(strings.TrimSpace(secret)))
	if err != nil {
		return 0, false
	}
	now := at.Unix() / totpStep
	for _, s := range []int64{now - 1, now, now + 1} {
		if subtle.ConstantTimeCompare([]byte(hotp(key, uint64(s))), []byte(code)) == 1 { //nolint:gosec // s is positive
			return s, true
		}
	}
	return 0, false
}

// NewRecoveryCodes returns n single-use codes like "k3f9-x2mq-7hwt".
func NewRecoveryCodes(n int) []string {
	const alphabet = "abcdefghjkmnpqrstuvwxyz23456789" // no 0/o, 1/l/i
	codes := make([]string, n)
	for i := range codes {
		b := make([]byte, 12)
		_, _ = rand.Read(b)
		var sb strings.Builder
		for j, c := range b {
			if j > 0 && j%4 == 0 {
				sb.WriteByte('-')
			}
			sb.WriteByte(alphabet[int(c)%len(alphabet)])
		}
		codes[i] = sb.String()
	}
	return codes
}

// NormalizeRecoveryCode makes typed codes comparable: lower case, no spaces or dashes.
func NormalizeRecoveryCode(code string) string {
	r := strings.NewReplacer(" ", "", "-", "")
	return strings.ToLower(r.Replace(strings.TrimSpace(code)))
}
