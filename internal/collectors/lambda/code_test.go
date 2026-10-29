// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: Apache-2.0

package lambda

import (
	"archive/zip"
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	"github.com/aws/aws-sdk-go-v2/service/lambda/types"
)

func zipOf(t *testing.T, files map[string][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, b := range files {
		h := &zip.FileHeader{Name: name, Method: zip.Deflate}
		if name == "bootstrap" {
			h.SetMode(0o755)
		}
		w, err := zw.CreateHeader(h)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write(b)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestZipPackages(t *testing.T) {
	jar := zipOf(t, map[string][]byte{"META-INF/maven/org.apache.logging.log4j/log4j-core/pom.properties": []byte("groupId=org.apache.logging.log4j\nartifactId=log4j-core\nversion=2.14.1\n")})
	self, err := os.ReadFile(os.Args[0]) //nolint:gosec // the test binary: a Go executable with build information
	if err != nil {
		t.Fatal(err)
	}
	code := zipOf(t, map[string][]byte{
		"index.js":                                                   []byte("exports.handler = async () => {}"),
		"node_modules/lodash/package.json":                           []byte(`{"name":"lodash","version":"4.17.15"}`),
		"node_modules/lodash/fp/package.json":                        []byte(`{"name":"lodash-fp","version":"0"}`),
		"node_modules/@aws-sdk/client-s3/package.json":               []byte(`{"name":"@aws-sdk/client-s3","version":"3.600.0"}`),
		"node_modules/a/node_modules/minimist/package.json":          []byte(`{"name":"minimist","version":"1.2.5"}`),
		"package-lock.json":                                          []byte(`{"packages":{"node_modules/ignored":{"version":"1.0.0"}}}`),
		"python/requests-2.31.0.dist-info/METADATA":                  []byte("Metadata-Version: 2.1\nName: requests\nVersion: 2.31.0\n\nbody"),
		"python/PyYAML-6.0.dist-info/METADATA":                       []byte("Name: PyYAML\nVersion: 6.0\n"),
		"lib/log4j-core-2.14.1.jar":                                  jar,
		"vendor/bundle/ruby/3.3.0/specifications/rack-3.1.8.gemspec": []byte("spec"),
		"app.deps.json":                                              []byte(`{"libraries":{"Newtonsoft.Json/13.0.1":{"type":"package"},"app/1.0.0":{"type":"project"}}}`),
		"bootstrap":                                                  self,
	})
	zr, err := zip.NewReader(bytes.NewReader(code), int64(len(code)))
	if err != nil {
		t.Fatal(err)
	}
	got, err := zipPackages(zr, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"pkg:npm/lodash@4.17.15", "pkg:npm/%40aws-sdk/client-s3@3.600.0", "pkg:npm/minimist@1.2.5", "pkg:pypi/requests@2.31.0",
		"pkg:pypi/pyyaml@6.0", "pkg:maven/org.apache.logging.log4j/log4j-core@2.14.1", "pkg:gem/rack@3.1.8",
		"pkg:nuget/Newtonsoft.Json@13.0.1",
	} {
		if !slices.Contains(got, want) {
			t.Errorf("missing %s in %v", want, got)
		}
	}
	if !slices.ContainsFunc(got, func(p string) bool { return strings.HasPrefix(p, "pkg:golang/stdlib@1.") }) {
		t.Errorf("no Go build information in %v", got)
	}
	for _, p := range got {
		if strings.Contains(p, "lodash-fp") || strings.Contains(p, "ignored") || strings.Contains(p, "app@") {
			t.Errorf("read %s", p)
		}
	}
	// Without node_modules, the lock file says what is installed.
	lock := zipOf(t, map[string][]byte{"package-lock.json": []byte(`{"packages":{"":{},"node_modules/express":{"version":"4.18.2"},"node_modules/jest":{"version":"29.0.0","dev":true}}}`)})
	zr, _ = zip.NewReader(bytes.NewReader(lock), int64(len(lock)))
	if got, _ := zipPackages(zr, 0); len(got) != 1 || got[0] != "pkg:npm/express@4.18.2" {
		t.Errorf("lock: %v", got)
	}
}

func TestCodeReader(t *testing.T) {
	code := zipOf(t, map[string][]byte{"node_modules/lodash/package.json": []byte(`{"name":"lodash","version":"4.17.15"}`)})
	layer := zipOf(t, map[string][]byte{"python/requests-2.31.0.dist-info/METADATA": []byte("Name: requests\nVersion: 2.31.0\n")})
	downloads := 0
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		downloads++
		switch r.URL.Path {
		case "/code.zip":
			_, _ = w.Write(code)
		case "/layer.zip":
			_, _ = w.Write(layer)
		default:
			w.WriteHeader(http.StatusForbidden)
		}
	}))
	defer srv.Close()
	r := newCodeReader(fakeLayers{location: srv.URL + "/layer.zip"})
	r.http = srv.Client()
	r.hostOK = func(string) bool { return true }
	r.startCollect()
	layers := []types.Layer{{Arn: aws.String("arn:aws:lambda:eu-west-1:1:layer:deps:3")}}
	digest := codeDigest("abc=", []string{"arn:aws:lambda:eu-west-1:1:layer:deps:3"})
	got, ok := r.read(context.Background(), digest, srv.URL+"/code.zip", layers)
	if !ok || got.Error != "" || !slices.Equal(got.Purls, []string{"pkg:npm/lodash@4.17.15", "pkg:pypi/requests@2.31.0"}) {
		t.Fatalf("read: %+v %v", got, ok)
	}
	// Read once per digest.
	r.startCollect()
	if again, _ := r.read(context.Background(), digest, srv.URL+"/code.zip", layers); downloads != 2 || !slices.Equal(again.Purls, got.Purls) {
		t.Errorf("read again: %d downloads", downloads)
	}
	if c := r.collected(); len(c) != 1 || c[0].Digest != digest {
		t.Errorf("collected: %+v", c)
	}
	// A failed download is said, without the presigned URL.
	r.startCollect()
	failed, _ := r.read(context.Background(), codeDigest("other=", nil), srv.URL+"/gone.zip?X-Amz-Signature=secret", nil)
	if !strings.Contains(failed.Error, "403") || strings.Contains(failed.Error, "secret") {
		t.Errorf("failed: %+v", failed)
	}
	// Only S3 is downloaded from.
	r.hostOK = newCodeReader(nil).hostOK
	r.startCollect()
	if other, _ := r.read(context.Background(), codeDigest("third=", nil), srv.URL+"/code.zip", nil); !strings.Contains(other.Error, "no S3 location") {
		t.Errorf("not S3: %+v", other)
	}
	// The budget spreads first reads over collects.
	r.startCollect()
	for i := range codePerCollect {
		r.read(context.Background(), codeDigest(string(rune('a'+i)), nil), "", nil)
	}
	if _, ok := r.read(context.Background(), codeDigest("over", nil), "", nil); ok {
		t.Error("read past the budget")
	}
}

type fakeLayers struct{ location string }

func (f fakeLayers) GetLayerVersionByArn(context.Context, *lambda.GetLayerVersionByArnInput, ...func(*lambda.Options)) (*lambda.GetLayerVersionByArnOutput, error) {
	return &lambda.GetLayerVersionByArnOutput{Content: &types.LayerVersionContentOutput{Location: aws.String(f.location)}}, nil
}

func TestCollectZipCode(t *testing.T) {
	code := zipOf(t, map[string][]byte{"node_modules/lodash/package.json": []byte(`{"name":"lodash","version":"4.17.15"}`)})
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(code) }))
	defer srv.Close()
	api := &fakeAPI{
		pages: [][]types.FunctionConfiguration{{
			{FunctionName: aws.String("thumbs"), FunctionArn: aws.String("arn:thumbs"), PackageType: types.PackageTypeZip, Runtime: types.RuntimeNodejs20x, CodeSha256: aws.String("abc=")},
			{FunctionName: aws.String("api"), FunctionArn: aws.String("arn:api"), PackageType: types.PackageTypeImage},
		}},
		code: map[string]*types.FunctionCodeLocation{
			"thumbs": {Location: aws.String(srv.URL + "/thumbs.zip")},
			"api":    {ImageUri: aws.String("1.dkr.ecr.eu-west-1.amazonaws.com/api:1.0")},
		},
	}
	c := NewWithAPI(api, "eu-west-1", nil)
	c.code = newCodeReader(nil)
	c.code.http, c.code.hostOK = srv.Client(), func(string) bool { return true }
	res, err := c.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var thumbs, other *string
	for _, w := range res.Workloads {
		switch w.Name {
		case "thumbs":
			thumbs = w.CodeDigest
		case "api":
			other = w.CodeDigest
		}
	}
	if thumbs == nil || *thumbs != codeDigest("abc=", nil) || other != nil {
		t.Fatalf("code digests: %v %v", thumbs, other)
	}
	if len(res.Code) != 1 || res.Code[0].Digest != *thumbs || !slices.Equal(res.Code[0].Purls, []string{"pkg:npm/lodash@4.17.15"}) {
		t.Errorf("code: %+v", res.Code)
	}
}
