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
	pages [][]types.FunctionConfiguration
	code  map[string]*types.FunctionCodeLocation
	tags  map[string]map[string]string
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
	return &lambda.GetFunctionOutput{Code: f.code[name], Tags: f.tags[name]}, nil
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
