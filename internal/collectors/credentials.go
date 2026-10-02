// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: Apache-2.0

package collectors

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// DefaultCredentialsDir is where Credential looks for files when
// GOLIASH_CREDENTIALS_DIR is not set (a mounted Kubernetes secret, for example).
const DefaultCredentialsDir = "/etc/goliash-agent/credentials" //nolint:gosec // a directory path, not a secret

// ErrNoCredential means a credentials_ref names nothing the agent can find.
var ErrNoCredential = errors.New("credential not found")

// Credential resolves a target's credentials_ref to a secret held by the agent.
// The server only ever sends the name. For ref "nomad-prod" it reads, in order:
//
//   - the environment variable GOLIASH_CREDENTIAL_NOMAD_PROD
//   - the file $GOLIASH_CREDENTIALS_DIR/nomad-prod (default /etc/goliash-agent/credentials)
//
// Surrounding whitespace is trimmed.
func Credential(ref string) (string, error) {
	if ref == "" || strings.ContainsAny(ref, `/\`) || ref == "." || ref == ".." {
		return "", fmt.Errorf("invalid credentials_ref %q", ref)
	}
	if v, ok := os.LookupEnv(CredentialEnv(ref)); ok {
		return strings.TrimSpace(v), nil
	}
	dir := os.Getenv("GOLIASH_CREDENTIALS_DIR")
	if dir == "" {
		dir = DefaultCredentialsDir
	}
	b, err := os.ReadFile(filepath.Join(dir, ref)) //nolint:gosec // ref is a plain file name, checked above
	if errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("%w: set %s or create %s", ErrNoCredential, CredentialEnv(ref), filepath.Join(dir, ref))
	}
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}

// CredentialEnv returns the environment variable consulted for ref.
func CredentialEnv(ref string) string {
	var b strings.Builder
	b.WriteString("GOLIASH_CREDENTIAL_")
	for _, r := range strings.ToUpper(ref) {
		if (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	return b.String()
}

// SplitImage splits "repo:tag@sha256:…" into the image reference without the digest
// and the digest. Swarm pins digests this way; other references pass through.
func SplitImage(ref string) (image, digest string) {
	image, digest, ok := strings.Cut(ref, "@")
	if !ok || !strings.HasPrefix(digest, "sha256:") || len(digest) != len("sha256:")+64 {
		return ref, ""
	}
	return image, digest
}
