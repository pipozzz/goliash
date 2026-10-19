// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package versions

import (
	"regexp"
	"strings"
)

// BumpResult is what BumpImageTag changed in one file.
type BumpResult struct {
	Content []byte
	Changes int  // tags changed
	Pinned  bool // the image is pinned by digest there: changing the tag alone would be wrong, so it was left
}

// imageWithTag finds image references with a tag in YAML, Kustomize or Helm values: "ghcr.io/acme/app:1.4.2",
// optionally pinned "@sha256:…".
var imageWithTag = regexp.MustCompile(`([A-Za-z0-9][A-Za-z0-9._\-/]*(?::[0-9]+)?/)?[A-Za-z0-9][A-Za-z0-9._\-]*:([A-Za-z0-9_][A-Za-z0-9._\-]{0,127})(@sha256:[a-f0-9]{64})?`)

// imageKey is a line naming an image without its tag: Kustomize's images (name, newName), Helm values
// (repository, image) and the like.
var imageKey = regexp.MustCompile(`^(\s*)(- )?\s*(name|newName|repository|image|imageName|imageRepository):\s*["']?([^"'\s#]+)["']?\s*(#.*)?$`)

// tagKey is a line giving that image's tag: Kustomize's newTag, Helm's tag.
var tagKey = regexp.MustCompile(`^(\s*)(- )?\s*(newTag|tag|imageTag|version):\s*(["']?)([^"'\s#]+)(["']?)(\s*(#.*)?)$`)

// BumpImageTag changes the tag of one image from one version to another in a GitOps file, wherever the file
// pins it: a reference "repo:from" (manifests, values), or a key naming the image with a tag key next to it in
// the same block (Kustomize images, Helm values). repo is the image's identity (ParseImage(...).Repo()); other
// images, and the same tag of other images, are left alone.
func BumpImageTag(content []byte, repo, from, to string) BumpResult {
	res := BumpResult{}
	if from == "" || to == "" || from == to {
		res.Content = content
		return res
	}
	lines := strings.Split(string(content), "\n")
	for i, line := range lines {
		lines[i] = imageWithTag.ReplaceAllStringFunc(line, func(m string) string {
			ref, digest, _ := strings.Cut(m, "@")
			r := ParseImage(ref)
			if r.Repo() != repo || r.Tag != from {
				return m
			}
			if digest != "" {
				res.Pinned = true
				return m
			}
			res.Changes++
			return strings.TrimSuffix(ref, from) + to
		})
	}
	for i, line := range lines {
		k := imageKey.FindStringSubmatch(line)
		if k == nil || ParseImage(k[4]).Repo() != repo || strings.Contains(k[4][strings.LastIndex(k[4], "/")+1:], ":") {
			continue
		}
		indent := len(k[1])
		if k[2] != "" { // "- name: x": the item's keys are indented past the dash
			indent += 2
		}
		for _, j := range blockLines(lines, i, indent) {
			t := tagKey.FindStringSubmatch(lines[j])
			if t == nil || t[5] != from {
				continue
			}
			at := strings.Index(lines[j], t[3]+":")
			lines[j] = lines[j][:at] + strings.Replace(lines[j][at:], from, to, 1)
			res.Changes++
		}
	}
	res.Content = []byte(strings.Join(lines, "\n"))
	return res
}

// blockLines are the lines of the YAML mapping that line i belongs to (keys at indent), a few each way: where a
// tag key next to an image key would be.
func blockLines(lines []string, i, indent int) []int {
	var out []int
	keyIndent := func(l string) (int, bool, bool) { // indentation of its key, whether it starts a list item, blank
		if strings.TrimSpace(l) == "" || strings.HasPrefix(strings.TrimSpace(l), "#") {
			return 0, false, true
		}
		n := len(l) - len(strings.TrimLeft(l, " "))
		if strings.HasPrefix(strings.TrimLeft(l, " "), "- ") {
			return n + 2, true, false
		}
		return n, false, false
	}
	for j := i + 1; j < len(lines) && j <= i+8; j++ {
		n, item, blank := keyIndent(lines[j])
		if blank {
			continue
		}
		if n < indent || item && n == indent { // the block ended, or the next list item began
			break
		}
		if n == indent {
			out = append(out, j)
		}
	}
	if _, item, _ := keyIndent(lines[i]); item {
		return out // the image key opens its item: nothing of it comes before
	}
	for j := i - 1; j >= 0 && j >= i-8; j-- {
		n, item, blank := keyIndent(lines[j])
		if blank {
			continue
		}
		if n < indent {
			break
		}
		if n == indent {
			out = append(out, j)
		}
		if item && n == indent { // the item's first key
			break
		}
	}
	return out
}

// PinnedTags are the tags a GitOps file pins the image at, in the forms BumpImageTag changes: "repo:tag"
// references, and a tag key next to a key naming the image. Digest-pinned references are left out.
func PinnedTags(content []byte, repo string) []string {
	var out []string
	add := func(t string) {
		for _, x := range out {
			if x == t {
				return
			}
		}
		out = append(out, t)
	}
	lines := strings.Split(string(content), "\n")
	for _, line := range lines {
		for _, m := range imageWithTag.FindAllString(line, -1) {
			ref, digest, _ := strings.Cut(m, "@")
			if r := ParseImage(ref); r.Repo() == repo && digest == "" && r.Tag != "" {
				add(r.Tag)
			}
		}
	}
	for i, line := range lines {
		k := imageKey.FindStringSubmatch(line)
		if k == nil || ParseImage(k[4]).Repo() != repo || strings.Contains(k[4][strings.LastIndex(k[4], "/")+1:], ":") {
			continue
		}
		indent := len(k[1])
		if k[2] != "" {
			indent += 2
		}
		for _, j := range blockLines(lines, i, indent) {
			if t := tagKey.FindStringSubmatch(lines[j]); t != nil {
				add(t[5])
			}
		}
	}
	return out
}
