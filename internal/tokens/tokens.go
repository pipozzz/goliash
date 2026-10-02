// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

// Package tokens creates and checks machine tokens.
//
// A token looks like glsh_agent_<30 random base62 characters, 178 bits><6 character CRC32 checksum>.
// The fixed prefix lets secret scanners find leaked tokens, and the checksum lets them,
// and the server, reject mistyped tokens without a database lookup. Only the SHA-256
// hash of a token is stored.
package tokens

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"hash/crc32"
	"strings"
)

// Kind is what a token may be used for.
type Kind string

// Token kinds.
const (
	Agent Kind = "agent"
	CI    Kind = "ci"
	API   Kind = "api"
)

const (
	prefix      = "glsh_"
	randomLen   = 30
	checksumLen = 6
	alphabet    = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"
)

// New returns a new token of the given kind and its hash.
func New(kind Kind) (token, hash string) {
	random := make([]byte, 0, randomLen)
	buf := make([]byte, 64)
	for len(random) < randomLen {
		_, _ = rand.Read(buf) // never fails on supported platforms
		for _, c := range buf {
			// 248 = 4*62: rejecting larger bytes keeps every character equally likely.
			if c < 248 && len(random) < randomLen {
				random = append(random, alphabet[c%62])
			}
		}
	}
	token = prefix + string(kind) + "_" + string(random) + checksum(string(random))
	return token, Hash(token)
}

// Hash returns the hex SHA-256 hash under which a token is stored.
func Hash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// Valid reports whether token is well formed, of the given kind, and has a correct checksum.
func Valid(token string, kind Kind) bool {
	rest, ok := strings.CutPrefix(token, prefix+string(kind)+"_")
	if !ok || len(rest) != randomLen+checksumLen {
		return false
	}
	random, sum := rest[:randomLen], rest[randomLen:]
	return checksum(random) == sum
}

func checksum(s string) string {
	n := crc32.ChecksumIEEE([]byte(s))
	out := make([]byte, checksumLen)
	for i := checksumLen - 1; i >= 0; i-- {
		out[i] = alphabet[n%uint32(len(alphabet))]
		n /= uint32(len(alphabet))
	}
	return string(out)
}
