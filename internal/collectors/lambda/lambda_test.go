// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: Apache-2.0

package lambda

import (
	"context"
	"errors"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	"github.com/aws/aws-sdk-go-v2/service/lambda/types"
)

func TestRuntimeImage(t *testing.T) {
	for runtime, want := range map[string]string{
		"python3.12":      "public.ecr.aws/lambda/python:3.12",
		"nodejs20.x":      "public.ecr.aws/lambda/nodejs:20",
		"java21":          "public.ecr.aws/lambda/java:21",
		"java8.al2":       "public.ecr.aws/lambda/java:8.al2",
		"dotnet8":         "public.ecr.aws/lambda/dotnet:8",
		"ruby3.3":         "public.ecr.aws/lambda/ruby:3.3",
		"go1.x":           "public.ecr.aws/lambda/go:1",
		"provided.al2023": "public.ecr.aws/lambda/provided:al2023",
		"dotnetcore3.1":   "",
		"provided":        "",
		"":                "",
	} {
		if got := RuntimeImage(runtime); got != want {
			t.Errorf("%q: %q, want %q", runtime, got, want)
		}
	}
}

type fakeAPI struct {
	pages       [][]types.FunctionConfiguration
	code        map[string]*types.FunctionCodeLocation
	tags        map[string]map[string]string
	aliases     map[string][]types.AliasConfiguration
	versions    map[string]map[string]*lambda.GetFunctionOutput // function -> version
	denyAliases bool
	gets        []string
}

func (f *fakeAPI) ListFunctions(_ context.Context, in *lambda.ListFunctionsInput, _ ...func(*lambda.Options)) (*lambda.ListFunctionsOutput, error) {
	i := 0
	if in.Marker != nil {
		i = 1
	}
	out := &lambda.ListFunctionsOutput{Functions: f.pages[i]}
	if i+1 < len(f.pages) {
		out.NextMarker = aws.String("next")
	}
	return out, nil
}

func (f *fakeAPI) GetFunction(_ context.Context, in *lambda.GetFunctionInput, _ ...func(*lambda.Options)) (*lambda.GetFunctionOutput, error) {
	name := aws.ToString(in.FunctionName)
	if name == "shop-broken" {
		return nil, errors.New("AccessDeniedException")
	}
	if q := aws.ToString(in.Qualifier); q != "" {
		f.gets = append(f.gets, name+":"+q)
		return f.versions[name][q], nil
	}
	return &lambda.GetFunctionOutput{Code: f.code[name], Tags: f.tags[name]}, nil
}

func (f *fakeAPI) ListAliases(_ context.Context, in *lambda.ListAliasesInput, _ ...func(*lambda.Options)) (*lambda.ListAliasesOutput, error) {
	if f.denyAliases {
		return nil, errors.New("operation error Lambda: ListAliases, AccessDeniedException: not authorized")
	}
	return &lambda.ListAliasesOutput{Aliases: f.aliases[aws.ToString(in.FunctionName)]}, nil
}

func fn(name string, pkg types.PackageType, runtime types.Runtime) types.FunctionConfiguration {
	return types.FunctionConfiguration{
		FunctionName: aws.String(name), FunctionArn: aws.String("arn:aws:lambda:eu-west-1:1:function:" + name),
		PackageType: pkg, Runtime: runtime,
	}
}

func TestCollect(t *testing.T) {
	digest := "sha256:" + "ab12cd34ab12cd34ab12cd34ab12cd34ab12cd34ab12cd34ab12cd34ab12cd34"
	api := &fakeAPI{
		pages: [][]types.FunctionConfiguration{
			{fn("shop-resize", types.PackageTypeZip, types.RuntimePython312), fn("other", types.PackageTypeZip, types.RuntimeNodejs20x)},
			{fn("shop-api", types.PackageTypeImage, ""), fn("shop-broken", types.PackageTypeZip, types.RuntimeJava21)},
		},
		code: map[string]*types.FunctionCodeLocation{"shop-api": {
			ImageUri:         aws.String("123.dkr.ecr.eu-west-1.amazonaws.com/shop-api:1.4.2"),
			ResolvedImageUri: aws.String("123.dkr.ecr.eu-west-1.amazonaws.com/shop-api@" + digest),
		}},
		tags: map[string]map[string]string{"shop-api": {"goliash.app": "shop"}},
	}
	res, err := NewWithAPI(api, "eu-west-1", []string{"shop-"}).Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.Complete || len(res.Errors) != 1 || len(res.Workloads) != 2 {
		t.Fatalf("result: %+v", res)
	}
	api1, resize := res.Workloads[0], res.Workloads[1]
	if api1.Name != "shop-api" || api1.Labels["goliash.app"] != "shop" || *api1.Namespace != "eu-west-1" ||
		api1.Containers[0].Image != "123.dkr.ecr.eu-west-1.amazonaws.com/shop-api:1.4.2" || *api1.Containers[0].Digest != digest {
		t.Fatalf("image function: %+v", api1)
	}
	if resize.Containers[0].Image != "public.ecr.aws/lambda/python:3.12" || resize.Containers[0].Name != "runtime" || resize.Containers[0].Running != 1 {
		t.Fatalf("zip function: %+v", resize)
	}
}

func alias(fn, name, version string) types.AliasConfiguration {
	return types.AliasConfiguration{
		Name: aws.String(name), FunctionVersion: aws.String(version),
		AliasArn: aws.String("arn:aws:lambda:eu-west-1:1:function:" + fn + ":" + name),
	}
}

func TestAliases(t *testing.T) {
	image := func(tag string) *lambda.GetFunctionOutput {
		return &lambda.GetFunctionOutput{
			Configuration: &types.FunctionConfiguration{PackageType: types.PackageTypeImage},
			Code:          &types.FunctionCodeLocation{ImageUri: aws.String("123.dkr.ecr.eu-west-1.amazonaws.com/api:" + tag)},
		}
	}
	api := &fakeAPI{
		pages: [][]types.FunctionConfiguration{{
			fn("api", types.PackageTypeImage, ""), fn("resize", types.PackageTypeZip, types.RuntimePython312),
		}},
		code: map[string]*types.FunctionCodeLocation{"api": {ImageUri: aws.String("123.dkr.ecr.eu-west-1.amazonaws.com/api:1.5.0")}},
		tags: map[string]map[string]string{"api": {"goliash.app": "shop"}},
		aliases: map[string][]types.AliasConfiguration{
			"api":    {alias("api", "dev", "$LATEST"), alias("api", "staging", "7"), alias("api", "live", "6"), alias("api", "canary", "7")},
			"resize": {alias("resize", "prod", "3")},
		},
		versions: map[string]map[string]*lambda.GetFunctionOutput{
			"api":    {"7": image("1.4.0"), "6": image("1.3.2")},
			"resize": {"3": {Configuration: &types.FunctionConfiguration{PackageType: types.PackageTypeZip, Runtime: types.RuntimePython311}}},
		},
	}
	c := NewWithAPI(api, "eu-west-1", nil)
	c.aliasEnvs = map[string]string{"live": "prod", "canary": "-"}
	res, err := c.Collect(context.Background())
	if err != nil || !res.Complete || len(res.Errors) != 0 {
		t.Fatalf("collect: %+v %v", res, err)
	}
	got := map[string]string{}
	for _, w := range res.Workloads {
		got[w.Name+"@"+w.Labels[aliasLabel]] = w.Labels["goliash.env"] + " " + w.Containers[0].Image
		if w.Name == "api" && w.Labels["goliash.app"] != "shop" {
			t.Errorf("tags lost: %+v", w.Labels)
		}
	}
	want := map[string]string{
		"api@dev":     "dev 123.dkr.ecr.eu-west-1.amazonaws.com/api:1.5.0",
		"api@staging": "staging 123.dkr.ecr.eu-west-1.amazonaws.com/api:1.4.0",
		"api@live":    "prod 123.dkr.ecr.eu-west-1.amazonaws.com/api:1.3.2",
		"resize@prod": "prod public.ecr.aws/lambda/python:3.11",
	}
	if len(got) != len(want) {
		t.Fatalf("workloads: %v", got)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s: %q, want %q", k, got[k], v)
		}
	}
	// Each version is read once, however many aliases point to it.
	if len(api.gets) != 3 { // api:7, api:6, resize:3
		t.Errorf("versions read: %v", api.gets)
	}

	// Without lambda:ListAliases: latest code, and a note.
	api.denyAliases = true
	res, err = NewWithAPI(api, "eu-west-1", nil).Collect(context.Background())
	if err != nil || len(res.Workloads) != 2 || len(res.Errors) != 1 || res.Workloads[0].Labels[aliasLabel] != "" {
		t.Fatalf("denied: %+v %v", res, err)
	}
}
