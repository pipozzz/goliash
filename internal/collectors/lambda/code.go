// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: Apache-2.0

package lambda

import (
	"archive/zip"
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"debug/buildinfo"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	"github.com/aws/aws-sdk-go-v2/service/lambda/types"

	"github.com/pipozzz/goliash/internal/collectors"
)

// A .zip function runs its own code on a runtime AWS patches: the code's dependencies (node_modules, Python
// packages, jars, the Go modules of a bootstrap binary) are what can be vulnerable, and no registry holds an SBOM
// of them. The agent downloads the code of each function version and its layers once, from the presigned S3 URL
// lambda:GetFunction and lambda:GetLayerVersion hand out, reads the package manifests in it without unpacking or
// running anything, and reports only the package URLs found. GOLIASH_LAMBDA_CODE=off turns it off.

// LayerAPI reads a layer version's code location: lambda:GetLayerVersion. Without it, layers are left out.
type LayerAPI interface {
	GetLayerVersionByArn(ctx context.Context, in *lambda.GetLayerVersionByArnInput, opts ...func(*lambda.Options)) (*lambda.GetLayerVersionByArnOutput, error)
}

const (
	maxDownload    = 300 << 20 // a function's zip is at most 250 MB unpacked; its compressed download less
	maxEntries     = 200000
	maxManifest    = 2 << 20  // a package.json, METADATA, deps.json …
	maxNested      = 64 << 20 // a jar inside the zip, or an executable read for Go build information
	maxPurls       = 20000
	codePerCollect = 4 // new code packages read per collect, so a first look at many functions spreads out
)

// codeReader reads and remembers the packages of code by its digest.
type codeReader struct {
	layers LayerAPI
	http   *http.Client
	hostOK func(host string) bool // where code may be downloaded from: S3's presigned URLs

	mu      sync.Mutex
	known   map[string]collectors.CodePackages
	budget  int
	current map[string]collectors.CodePackages // what this collect's functions run, as read
}

func newCodeReader(layers LayerAPI) *codeReader {
	return &codeReader{
		layers: layers, http: &http.Client{Timeout: 3 * time.Minute}, known: map[string]collectors.CodePackages{},
		hostOK: func(host string) bool { return strings.HasSuffix(host, ".amazonaws.com") },
	}
}

// codeDigest identifies what a function version runs: its code and the layer versions it uses.
func codeDigest(codeSHA string, layers []string) string {
	l := append([]string(nil), layers...)
	sort.Strings(l)
	sum := sha256.Sum256([]byte(codeSHA + "\n" + strings.Join(l, "\n")))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// startCollect gives a collect its budget of new reads.
func (r *codeReader) startCollect() {
	r.mu.Lock()
	r.budget, r.current = codePerCollect, map[string]collectors.CodePackages{}
	r.mu.Unlock()
}

// collected is the packages of the code this collect's functions run, for the digests read so far.
func (r *codeReader) collected() []collectors.CodePackages {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]collectors.CodePackages, 0, len(r.current))
	for _, p := range r.current {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Digest < out[j].Digest })
	return out
}

// read returns the packages of a function version's code, reading it when it is new and the budget allows.
func (r *codeReader) read(ctx context.Context, digest, location string, layers []types.Layer) (collectors.CodePackages, bool) {
	r.mu.Lock()
	if p, ok := r.known[digest]; ok {
		r.current[digest] = p
		r.mu.Unlock()
		return p, true
	}
	if r.budget <= 0 {
		r.mu.Unlock()
		return collectors.CodePackages{}, false
	}
	r.budget--
	r.mu.Unlock()

	res := collectors.CodePackages{Digest: digest}
	seen := map[string]bool{}
	var problems []string
	add := func(where, location string) {
		purls, err := r.packagesAt(ctx, location)
		if err != nil {
			problems = append(problems, where+": "+err.Error())
		}
		for _, p := range purls {
			seen[p] = true
		}
	}
	add("code", location)
	for _, l := range layers {
		arn := aws.ToString(l.Arn)
		if r.layers == nil {
			problems = append(problems, "layer "+arn+": not read")
			continue
		}
		lv, err := r.layers.GetLayerVersionByArn(ctx, &lambda.GetLayerVersionByArnInput{Arn: l.Arn})
		if err != nil {
			problems = append(problems, "layer "+arn+": "+err.Error())
			continue
		}
		if lv.Content != nil {
			add("layer "+arn, aws.ToString(lv.Content.Location))
		}
	}
	if ctx.Err() != nil {
		return collectors.CodePackages{}, false
	}
	for p := range seen {
		res.Purls = append(res.Purls, p)
	}
	sort.Strings(res.Purls)
	res.Purls = res.Purls[:min(len(res.Purls), maxPurls)]
	res.Error = strings.Join(problems, "; ")
	r.mu.Lock()
	r.known[digest], r.current[digest] = res, res
	r.mu.Unlock()
	return res, true
}

// packagesAt downloads a zip from its presigned S3 URL into a temporary file and reads its packages.
func (r *codeReader) packagesAt(ctx context.Context, location string) ([]string, error) {
	u, err := url.Parse(location)
	if err != nil || u.Scheme != "https" || !r.hostOK(u.Hostname()) {
		return nil, errors.New("no S3 location for the code")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, location, nil)
	if err != nil {
		return nil, err
	}
	resp, err := r.http.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err // without the presigned URL
		}
		return nil, fmt.Errorf("download: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download: %s", resp.Status)
	}
	f, err := os.CreateTemp("", "goliash-lambda-*.zip")
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close(); _ = os.Remove(f.Name()) }()
	n, err := io.Copy(f, io.LimitReader(resp.Body, maxDownload+1))
	if err != nil {
		return nil, fmt.Errorf("download: %w", err)
	}
	if n > maxDownload {
		return nil, errors.New("code larger than 300 MB: not read")
	}
	zr, err := zip.NewReader(f, n)
	if err != nil {
		return nil, fmt.Errorf("not a zip: %w", err)
	}
	return zipPackages(zr, 0)
}

// zipPackages reads the package URLs of the manifests in a zip: npm, PyPI, Maven (jars, nested ones too), Go
// (executables' build information), RubyGems and NuGet.
func zipPackages(zr *zip.Reader, depth int) ([]string, error) {
	if len(zr.File) > maxEntries {
		return nil, fmt.Errorf("%d files: too many to read", len(zr.File))
	}
	var out []string
	hasModules := false
	for _, f := range zr.File {
		if strings.Contains(f.Name, "node_modules/") {
			hasModules = true
			break
		}
	}
	for _, f := range zr.File {
		name := strings.TrimPrefix(f.Name, "/")
		base := path.Base(name)
		switch {
		case f.FileInfo().IsDir():
		case base == "package.json" && strings.Contains(name, "node_modules/"):
			if p := npmPackage(name, read(f, maxManifest)); p != "" {
				out = append(out, p)
			}
		case base == "package-lock.json" && !hasModules && !strings.Contains(name, "node_modules/"):
			out = append(out, npmLock(read(f, 64<<20))...)
		case base == "METADATA" && strings.HasSuffix(path.Dir(name), ".dist-info"), base == "PKG-INFO" && strings.HasSuffix(path.Dir(name), ".egg-info"):
			if p := pypiPackage(read(f, maxManifest)); p != "" {
				out = append(out, p)
			}
		case base == "pom.properties" && strings.Contains(name, "META-INF/maven/"):
			if p := mavenPackage(read(f, maxManifest)); p != "" {
				out = append(out, p)
			}
		case strings.HasSuffix(base, ".jar") && depth < 2 && f.UncompressedSize64 <= maxNested:
			b := read(f, maxNested)
			if jr, err := zip.NewReader(bytes.NewReader(b), int64(len(b))); err == nil {
				inner, _ := zipPackages(jr, depth+1)
				out = append(out, inner...)
			}
		case strings.HasSuffix(base, ".gemspec") && strings.Contains(name, "/specifications/"):
			if p := gemPackage(base); p != "" {
				out = append(out, p)
			}
		case strings.HasSuffix(base, ".deps.json"):
			out = append(out, nugetPackages(read(f, 16<<20))...)
		case executable(f) && f.UncompressedSize64 <= maxNested:
			out = append(out, goPackages(read(f, maxNested))...)
		}
	}
	return out, nil
}

// read reads a zip entry, up to limit bytes; nil when it is larger or unreadable.
func read(f *zip.File, limit int64) []byte {
	if f.UncompressedSize64 > uint64(limit) { //nolint:gosec // limit is one of the positive constants above
		return nil
	}
	rc, err := f.Open()
	if err != nil {
		return nil
	}
	defer func() { _ = rc.Close() }()
	b, err := io.ReadAll(io.LimitReader(rc, limit+1))
	if err != nil || int64(len(b)) > limit {
		return nil
	}
	return b
}

// executable reports whether an entry may be a Go binary: executable, or the custom runtime's bootstrap.
func executable(f *zip.File) bool {
	return path.Base(f.Name) == "bootstrap" || (f.Mode()&0o111 != 0 && !strings.Contains(path.Base(f.Name), "."))
}

func npmName(name string) string {
	if strings.HasPrefix(name, "@") {
		return "%40" + name[1:]
	}
	return name
}

// npmPackage reads node_modules/<name>/package.json (or node_modules/@scope/<name>/…), not files deeper in a
// package.
func npmPackage(name string, b []byte) string {
	i := strings.LastIndex(name, "node_modules/")
	rest := strings.TrimSuffix(name[i+len("node_modules/"):], "/package.json")
	if parts := strings.Split(rest, "/"); len(parts) > 2 || (len(parts) == 2 && !strings.HasPrefix(parts[0], "@")) {
		return ""
	}
	var p struct{ Name, Version string }
	if json.Unmarshal(b, &p) != nil || p.Name == "" || p.Version == "" || p.Name != rest {
		return ""
	}
	return "pkg:npm/" + npmName(p.Name) + "@" + p.Version
}

// npmLock reads a package-lock.json (lockfile versions 2 and 3) when the code ships no node_modules.
func npmLock(b []byte) []string {
	var lock struct {
		Packages map[string]struct {
			Version string `json:"version"`
			Dev     bool   `json:"dev"`
		} `json:"packages"`
	}
	if json.Unmarshal(b, &lock) != nil {
		return nil
	}
	var out []string
	for key, p := range lock.Packages {
		i := strings.LastIndex(key, "node_modules/")
		if i < 0 || p.Version == "" || p.Dev {
			continue
		}
		out = append(out, "pkg:npm/"+npmName(key[i+len("node_modules/"):])+"@"+p.Version)
	}
	return out
}

// headers reads "Key: value" lines up to the first empty one.
func headers(b []byte) map[string]string {
	out := map[string]string{}
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			break
		}
		if k, v, ok := strings.Cut(line, ":"); ok {
			k = strings.TrimSpace(k)
			if _, set := out[k]; !set {
				out[k] = strings.TrimSpace(v)
			}
		}
	}
	return out
}

// pypiPackage reads a dist-info METADATA or egg-info PKG-INFO.
func pypiPackage(b []byte) string {
	h := headers(b)
	name, version := h["Name"], h["Version"]
	if name == "" || version == "" {
		return ""
	}
	name = strings.ToLower(strings.NewReplacer("_", "-", ".", "-").Replace(name))
	return "pkg:pypi/" + name + "@" + version
}

// mavenPackage reads a jar's META-INF/maven/…/pom.properties.
func mavenPackage(b []byte) string {
	props := map[string]string{}
	for _, line := range strings.Split(string(b), "\n") {
		if k, v, ok := strings.Cut(strings.TrimSpace(line), "="); ok && !strings.HasPrefix(k, "#") {
			props[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
	}
	g, a, v := props["groupId"], props["artifactId"], props["version"]
	if g == "" || a == "" || v == "" {
		return ""
	}
	return "pkg:maven/" + g + "/" + a + "@" + v
}

// gemPackage reads a gem's name and version from its specification's file name: rack-3.1.8.gemspec.
func gemPackage(base string) string {
	s := strings.TrimSuffix(base, ".gemspec")
	i := strings.LastIndex(s, "-")
	if i <= 0 || i == len(s)-1 || s[i+1] < '0' || s[i+1] > '9' {
		return ""
	}
	return "pkg:gem/" + s[:i] + "@" + s[i+1:]
}

// nugetPackages reads a .NET deps.json: its libraries of type package.
func nugetPackages(b []byte) []string {
	var deps struct {
		Libraries map[string]struct {
			Type string `json:"type"`
		} `json:"libraries"`
	}
	if json.Unmarshal(b, &deps) != nil {
		return nil
	}
	var out []string
	for key, l := range deps.Libraries {
		name, version, ok := strings.Cut(key, "/")
		if ok && l.Type == "package" {
			out = append(out, "pkg:nuget/"+name+"@"+version)
		}
	}
	return out
}

// goPackages reads a Go binary's build information: its modules, and the Go standard library it was built with.
func goPackages(b []byte) []string {
	info, err := buildinfo.Read(bytes.NewReader(b))
	if err != nil {
		return nil
	}
	var out []string
	if v := strings.TrimPrefix(info.GoVersion, "go"); v != "" {
		out = append(out, "pkg:golang/stdlib@"+strings.SplitN(v, " ", 2)[0])
	}
	for _, d := range info.Deps {
		m := d
		if d.Replace != nil {
			m = d.Replace
		}
		if m.Version != "" && m.Version != "(devel)" {
			out = append(out, "pkg:golang/"+m.Path+"@"+m.Version)
		}
	}
	return out
}
