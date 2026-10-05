// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: Apache-2.0

package registry

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

// LabelSource is the OCI annotation and image label naming the source repository.
const LabelSource = "org.opencontainers.image.source"

const (
	mediaIndex       = "application/vnd.oci.image.index.v1+json"
	mediaDockerList  = "application/vnd.docker.distribution.manifest.list.v2+json"
	mediaManifest    = "application/vnd.oci.image.manifest.v1+json"
	mediaDockerImage = "application/vnd.docker.distribution.manifest.v2+json"
)

type manifest struct {
	MediaType   string            `json:"mediaType"`
	Annotations map[string]string `json:"annotations"`
	// Index or manifest list.
	Manifests []struct {
		Digest   string `json:"digest"`
		Platform *struct {
			OS           string `json:"os"`
			Architecture string `json:"architecture"`
		} `json:"platform"`
	} `json:"manifests"`
	// Image manifest.
	Config struct {
		Digest string `json:"digest"`
	} `json:"config"`
}

// ImageSource returns the source repository URL an image declares for reference (a
// tag or digest): the org.opencontainers.image.source annotation of its index or
// manifest, else the label in its linux/amd64 image config. It returns "" when the
// image declares none.
func (c *Client) ImageSource(ctx context.Context, repository, reference string, creds Credentials) (string, error) {
	host, repo, err := splitRepository(repository)
	if err != nil {
		return "", err
	}
	base := fmt.Sprintf("%s://%s/v2/%s", c.Scheme, host, repo)
	accept := strings.Join([]string{mediaIndex, mediaDockerList, mediaManifest, mediaDockerImage}, ", ")

	m, err := c.manifest(ctx, base+"/manifests/"+reference, repo, creds, accept)
	if err != nil {
		return "", err
	}
	if src := m.Annotations[LabelSource]; src != "" {
		return src, nil
	}
	if len(m.Manifests) > 0 {
		digest := pickPlatform(m)
		if digest == "" {
			return "", nil
		}
		if m, err = c.manifest(ctx, base+"/manifests/"+digest, repo, creds, accept); err != nil {
			return "", err
		}
		if src := m.Annotations[LabelSource]; src != "" {
			return src, nil
		}
	}
	if m.Config.Digest == "" {
		return "", nil
	}
	body, _, err := c.get(ctx, base+"/blobs/"+m.Config.Digest, repo, creds, "application/json")
	if err != nil {
		return "", err
	}
	var cfg struct {
		Config struct {
			Labels map[string]string `json:"Labels"`
		} `json:"config"`
	}
	if err := json.Unmarshal(body, &cfg); err != nil {
		return "", fmt.Errorf("image config of %s: %w", repository, err)
	}
	return cfg.Config.Labels[LabelSource], nil
}

// TagDigest returns the digest of reference's manifest (or index) with a HEAD
// request, which registries such as Docker Hub do not count as a pull.
func (c *Client) TagDigest(ctx context.Context, repository, reference string, creds Credentials) (string, error) {
	host, repo, err := splitRepository(repository)
	if err != nil {
		return "", err
	}
	accept := strings.Join([]string{mediaIndex, mediaDockerList, mediaManifest, mediaDockerImage}, ", ")
	h, err := c.head(ctx, fmt.Sprintf("%s://%s/v2/%s/manifests/%s", c.Scheme, host, repo, reference), repo, creds, accept)
	if err != nil {
		return "", err
	}
	return h.Get("Docker-Content-Digest"), nil
}

// ManifestDigests returns the digests reference (a tag) stands for: its manifest's
// own digest and, for a multi-platform index, the digest of each platform's manifest.
// A running image's digest matches one of them, whichever the runtime recorded.
func (c *Client) ManifestDigests(ctx context.Context, repository, reference string, creds Credentials) ([]string, error) {
	host, repo, err := splitRepository(repository)
	if err != nil {
		return nil, err
	}
	accept := strings.Join([]string{mediaIndex, mediaDockerList, mediaManifest, mediaDockerImage}, ", ")
	body, _, err := c.get(ctx, fmt.Sprintf("%s://%s/v2/%s/manifests/%s", c.Scheme, host, repo, reference), repo, creds, accept)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(body)
	out := []string{"sha256:" + hex.EncodeToString(sum[:])}
	var m manifest
	if err := json.Unmarshal(body, &m); err != nil {
		return nil, fmt.Errorf("manifest: %w", err)
	}
	for _, child := range m.Manifests {
		out = append(out, child.Digest)
	}
	return out, nil
}

func (c *Client) manifest(ctx context.Context, rawURL, repo string, creds Credentials, accept string) (manifest, error) {
	var m manifest
	body, _, err := c.get(ctx, rawURL, repo, creds, accept)
	if err != nil {
		return m, err
	}
	if err := json.Unmarshal(body, &m); err != nil {
		return m, fmt.Errorf("manifest: %w", err)
	}
	return m, nil
}

// pickPlatform chooses linux/amd64 from an index, else the first image that is not
// an attestation ("unknown/unknown").
func pickPlatform(m manifest) string {
	first := ""
	for _, d := range m.Manifests {
		if d.Platform == nil || d.Platform.OS == "unknown" {
			continue
		}
		if d.Platform.OS == "linux" && d.Platform.Architecture == "amd64" {
			return d.Digest
		}
		if first == "" {
			first = d.Digest
		}
	}
	return first
}

// GitHubRepository extracts "owner/repo" from a source URL such as
// https://github.com/traefik/traefik, git@github.com:acme/app.git or
// https://github.com/acme/app/tree/main/cmd. It returns "" for other hosts.
func GitHubRepository(source string) string {
	s := strings.TrimSpace(source)
	s = strings.TrimPrefix(s, "git+")
	for _, p := range []string{"https://", "http://", "ssh://", "git://"} {
		s = strings.TrimPrefix(s, p)
	}
	s = strings.TrimPrefix(s, "git@")
	s = strings.TrimPrefix(s, "www.")
	rest, ok := strings.CutPrefix(s, "github.com/")
	if !ok {
		if rest, ok = strings.CutPrefix(s, "github.com:"); !ok {
			return ""
		}
	}
	rest, _, _ = strings.Cut(rest, "#") // build context: repo.git#commit:path
	rest, _, _ = strings.Cut(rest, "?")
	parts := strings.SplitN(rest, "/", 3)
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		return ""
	}
	owner, repo := parts[0], strings.TrimSuffix(parts[1], ".git")
	for _, part := range []string{owner, repo} {
		if strings.IndexFunc(part, notRepoChar) >= 0 {
			return ""
		}
	}
	if repo == "" {
		return ""
	}
	return owner + "/" + repo
}

// notRepoChar reports runes GitHub does not allow in owner and repository names.
func notRepoChar(r rune) bool {
	return (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '-' && r != '_' && r != '.'
}

// GitLabProject extracts "host/group/project" from a source URL on one of hosts, such
// as https://gitlab.com/group/sub/project or https://gitlab.com/group/project/-/tree/main.
// It returns "" for other hosts.
func GitLabProject(source string, hosts []string) string {
	s := strings.TrimSpace(source)
	s = strings.TrimPrefix(s, "git+")
	for _, p := range []string{"https://", "http://"} {
		s = strings.TrimPrefix(s, p)
	}
	s, _, _ = strings.Cut(s, "#")
	s, _, _ = strings.Cut(s, "?")
	s, _, _ = strings.Cut(s, "/-/")
	host, path, ok := strings.Cut(s, "/")
	if !ok || !slices.Contains(hosts, host) {
		return ""
	}
	path = strings.TrimSuffix(strings.Trim(path, "/"), ".git")
	parts := strings.Split(path, "/")
	if len(parts) < 2 {
		return ""
	}
	for _, part := range parts {
		if part == "" || strings.IndexFunc(part, notRepoChar) >= 0 {
			return ""
		}
	}
	return host + "/" + path
}
