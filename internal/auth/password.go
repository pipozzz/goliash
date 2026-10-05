// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"
)

// argon2id parameters: the OWASP baseline (19 MiB, 2 passes, 1 lane), cheap
// enough for a small server and far slower to guess than any fast hash.
const (
	argonMemory  = 19 * 1024
	argonTime    = 2
	argonThreads = 1
	argonKeyLen  = 32

	// MinPasswordLength is the shortest password accepted.
	MinPasswordLength = 12
	maxPasswordLength = 256
)

// ErrWeakPassword explains why a new password is not accepted.
var ErrWeakPassword = errors.New("weak password")

// CheckPassword returns an ErrWeakPassword error describing what is wrong with a new
// password, or nil.
func CheckPassword(password, email string) error {
	n := utf8.RuneCountInString(password)
	switch {
	case n < MinPasswordLength:
		return fmt.Errorf("%w: use at least %d characters", ErrWeakPassword, MinPasswordLength)
	case len(password) > maxPasswordLength:
		return fmt.Errorf("%w: use at most %d characters", ErrWeakPassword, maxPasswordLength)
	case strings.EqualFold(password, email):
		return fmt.Errorf("%w: do not use your e-mail address", ErrWeakPassword)
	case strings.Count(password, password[:1]) == len(password):
		return fmt.Errorf("%w: do not repeat one character", ErrWeakPassword)
	}
	return nil
}

// HashPassword returns an argon2id hash in the PHC string format.
func HashPassword(password string) string {
	salt := make([]byte, 16)
	_, _ = rand.Read(salt)
	key := argon2.IDKey([]byte(password), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s", argon2.Version, argonMemory, argonTime, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key))
}

// VerifyPassword reports whether password matches a hash from HashPassword. It reads
// the parameters from the hash, so stronger settings later keep old hashes working.
func VerifyPassword(password, hash string) bool {
	parts := strings.Split(hash, "$")
	if len(parts) != 6 || parts[1] != "argon2id" || len(password) > maxPasswordLength {
		return false
	}
	var version int
	var memory, passes uint32
	var threads uint8
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return false
	}
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &passes, &threads); err != nil ||
		memory == 0 || memory > 1<<20 || passes == 0 || passes > 10 || threads == 0 {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil || len(want) == 0 {
		return false
	}
	got := argon2.IDKey([]byte(password), salt, passes, memory, threads, uint32(len(want))) //nolint:gosec // len(want) is a key length
	return subtle.ConstantTimeCompare(got, want) == 1
}

// dummyHash is verified when an e-mail has no account or no password, so a wrong
// address takes as long as a wrong password.
var dummyHash = HashPassword("goliash-no-such-password")
