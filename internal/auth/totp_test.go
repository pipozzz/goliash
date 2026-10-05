// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package auth

import (
	"encoding/base32"
	"strings"
	"testing"
	"time"
)

// The RFC 6238 test vectors for SHA-1 (8 digits there; the last 6 here).
func TestTOTPRFC6238(t *testing.T) {
	secret := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString([]byte("12345678901234567890"))
	for unix, want := range map[int64]string{
		59: "287082", 1111111109: "081804", 1111111111: "050471", 1234567890: "005924", 2000000000: "279037",
	} {
		got, err := TOTPCode(secret, time.Unix(unix, 0))
		if err != nil || got != want {
			t.Errorf("t=%d: %s %v, want %s", unix, got, err, want)
		}
	}
}

func TestVerifyTOTP(t *testing.T) {
	secret := NewTOTPSecret()
	now := time.Unix(1_800_000_000, 0)
	for _, d := range []time.Duration{-30 * time.Second, 0, 30 * time.Second} {
		code, _ := TOTPCode(secret, now.Add(d))
		if _, ok := VerifyTOTP(secret, code[:3]+" "+code[3:], now); !ok {
			t.Errorf("drift %v rejected", d)
		}
	}
	old, _ := TOTPCode(secret, now.Add(-2*time.Minute))
	if _, ok := VerifyTOTP(secret, old, now); ok {
		t.Error("old code accepted")
	}
	for _, bad := range []string{"", "12345", "1234567", "abcdef"} {
		if _, ok := VerifyTOTP(secret, bad, now); ok {
			t.Errorf("%q accepted", bad)
		}
	}
	if u := TOTPURI(secret, "Goliash", "ana@example.com"); !strings.HasPrefix(u, "otpauth://totp/Goliash:ana@example.com?") || !strings.Contains(u, "secret="+secret) {
		t.Errorf("uri %s", u)
	}
}

func TestRecoveryCodes(t *testing.T) {
	codes := NewRecoveryCodes(10)
	seen := map[string]bool{}
	for _, c := range codes {
		if len(c) != 14 || strings.Count(c, "-") != 2 || seen[c] {
			t.Fatalf("code %q", c)
		}
		seen[c] = true
	}
	if NormalizeRecoveryCode(" K3F9-X2MQ 7HWT ") != "k3f9x2mq7hwt" {
		t.Fatal("normalize")
	}
}
