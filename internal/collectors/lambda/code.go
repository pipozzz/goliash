// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: Apache-2.0

package lambda

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
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
	"github.com/pipozzz/goliash/internal/pkgscan"
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
	return zipPackages(zr)
}

// zipPackages reads the package URLs of the manifests in a zip, as pkgscan finds them.
func zipPackages(zr *zip.Reader) ([]string, error) {
	if len(zr.File) > maxEntries {
		return nil, fmt.Errorf("%d files: too many to read", len(zr.File))
	}
	var s pkgscan.Scanner
	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}
		exec := path.Base(f.Name) == "bootstrap" || f.Mode()&0o111 != 0
		if s.IsExecutable(f.Name, exec) {
			if rc, err := f.Open(); err == nil {
				s.AddStream(rc)
				_ = rc.Close()
			}
			continue
		}
		limit := s.Wants(f.Name, int64(f.UncompressedSize64), exec) //nolint:gosec // sizes in a zip fit
		if limit == 0 {
			continue
		}
		if b := pkgscan.Read(f, limit); b != nil {
			s.Add(f.Name, b)
		}
	}
	return s.Purls(), nil
}
