// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package versions

import (
	"crypto/rand"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/pipozzz/goliash/internal/store"
)

// BOM is a CycloneDX 1.6 bill of materials of what runs: one component per image, with where it runs as
// properties and, where the registry holds an SBOM for it, its packages nested inside.
type BOM struct {
	BOMFormat    string         `json:"bomFormat"`
	SpecVersion  string         `json:"specVersion"`
	SerialNumber string         `json:"serialNumber"`
	Version      int            `json:"version"`
	Metadata     BOMMetadata    `json:"metadata"`
	Components   []BOMComponent `json:"components"`
}

// BOMMetadata says when and by what the BOM was made, and what it describes.
type BOMMetadata struct {
	Timestamp string `json:"timestamp"`
	Tools     struct {
		Components []BOMComponent `json:"components"`
	} `json:"tools"`
	Component  BOMComponent  `json:"component"`
	Properties []BOMProperty `json:"properties,omitempty"`
}

// BOMComponent is an image, a host, a package, or the tool.
type BOMComponent struct {
	Type       string         `json:"type"`
	BOMRef     string         `json:"bom-ref,omitempty"`
	Name       string         `json:"name"`
	Version    string         `json:"version,omitempty"`
	Purl       string         `json:"purl,omitempty"`
	Hashes     []BOMHash      `json:"hashes,omitempty"`
	Licenses   []BOMLicense   `json:"licenses,omitempty"`
	Properties []BOMProperty  `json:"properties,omitempty"`
	Components []BOMComponent `json:"components,omitempty"`
}

// BOMLicense is a package's license: an SPDX expression when it combines licenses, else the license by name.
type BOMLicense struct {
	Expression string `json:"expression,omitempty"`
	License    *struct {
		Name string `json:"name"`
	} `json:"license,omitempty"`
}

func bomLicenses(l string) []BOMLicense {
	switch {
	case l == "":
		return nil
	case strings.ContainsAny(l, " ()"):
		return []BOMLicense{{Expression: l}}
	}
	one := BOMLicense{License: &struct {
		Name string `json:"name"`
	}{l}}
	return []BOMLicense{one}
}

// BOMHash is a component's digest.
type BOMHash struct {
	Alg     string `json:"alg"`
	Content string `json:"content"`
}

// BOMProperty is a name/value pair; names in the goliash: namespace.
type BOMProperty struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// InventoryBOM builds the CycloneDX document for items, an inventory of workspace taken at at (nil: now).
// sboms holds the packages known per repo + "@" + digest.
func InventoryBOM(items []InventoryItem, sboms map[string]store.ImageSBOM, workspace, toolVersion string, at *time.Time, now time.Time) BOM {
	b := BOM{BOMFormat: "CycloneDX", SpecVersion: "1.6", SerialNumber: "urn:uuid:" + uuid4(), Version: 1}
	b.Metadata.Timestamp = now.UTC().Format(time.RFC3339)
	b.Metadata.Tools.Components = []BOMComponent{{Type: "application", Name: "goliash", Version: toolVersion}}
	b.Metadata.Component = BOMComponent{Type: "application", BOMRef: "goliash-workspace", Name: workspace + ": running software"}
	if at != nil {
		b.Metadata.Properties = []BOMProperty{{Name: "goliash:at", Value: at.UTC().Format(time.RFC3339)}}
	}
	byRef := map[string]*BOMComponent{}
	var order []string
	for _, i := range items {
		ref := ParseImage(i.Image)
		repo := ref.Repo()
		key := repo + "@" + i.Digest
		if i.Digest == "" {
			key = repo + ":" + i.Tag
		}
		c, ok := byRef[key]
		if !ok {
			c = &BOMComponent{BOMRef: key, Name: repo, Version: i.Tag}
			switch {
			case i.Platform == "host" && i.Kind == "host_os":
				c.Type = "operating-system"
			case i.Platform == "host" || PackageRepo(repo):
				c.Type, c.Name = "application", strings.TrimPrefix(repo, "pkg.goliash/")
			case i.Platform == "manual":
				c.Type = "application"
			default:
				c.Type, c.Purl = "container", ociPurl(repo, i.Tag, i.Digest)
				if alg, sum, ok := strings.Cut(i.Digest, ":"); ok && alg == "sha256" {
					c.Hashes = []BOMHash{{Alg: "SHA-256", Content: sum}}
				}
				sbom := sboms[repo+"@"+i.Digest]
				for _, p := range sbom.Purls {
					c.Components = append(c.Components, BOMComponent{
						Type: "library", BOMRef: key + "|" + p, Name: purlPackage(p), Version: purlVersion(p), Purl: p,
						Licenses: bomLicenses(sbom.Licenses[p]),
					})
				}
			}
			byRef[key] = c
			order = append(order, key)
		}
		for _, p := range []BOMProperty{
			{"goliash:environment", i.Environment},
			{"goliash:target", i.Target},
			{"goliash:platform", i.Platform},
			{"goliash:service", i.Service},
			{"goliash:workload", strings.Trim(i.Namespace+"/"+i.Workload, "/")},
		} {
			if p.Value != "" && !hasProperty(c.Properties, p) {
				c.Properties = append(c.Properties, p)
			}
		}
	}
	sort.Strings(order)
	b.Components = make([]BOMComponent, 0, len(order))
	for _, k := range order {
		b.Components = append(b.Components, *byRef[k])
	}
	return b
}

func hasProperty(list []BOMProperty, p BOMProperty) bool {
	for _, x := range list {
		if x == p {
			return true
		}
	}
	return false
}

// ociPurl is the package URL of an image: pkg:oci/<name>@<digest>?repository_url=<repo>&tag=<tag>.
func ociPurl(repo, tag, digest string) string {
	name := repo[strings.LastIndex(repo, "/")+1:]
	p := "pkg:oci/" + url.PathEscape(name)
	if digest != "" {
		p += "@" + url.QueryEscape(digest)
	}
	q := url.Values{"repository_url": {repo}}
	if tag != "" {
		q.Set("tag", tag)
	}
	return p + "?" + q.Encode()
}

// purlPackage is a package URL's namespace and name, e.g. debian/openssl.
func purlPackage(p string) string {
	p = strings.SplitN(p, "?", 2)[0]
	p, _, _ = strings.Cut(p, "@")
	if _, rest, ok := strings.Cut(p, "/"); ok { // drop pkg:type
		p = rest
	}
	if u, err := url.PathUnescape(p); err == nil {
		p = u
	}
	return p
}

func uuid4() string {
	var b [16]byte
	_, _ = rand.Read(b[:]) // crypto/rand.Read never fails
	b[6], b[8] = b[6]&0x0f|0x40, b[8]&0x3f|0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
