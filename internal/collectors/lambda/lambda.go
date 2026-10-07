// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: Apache-2.0

package lambda

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	"github.com/aws/aws-sdk-go-v2/service/lambda/types"

	"github.com/pipozzz/goliash/internal/collectors"
	"github.com/pipozzz/goliash/pkg/agentproto"
)

// API is the subset of the Lambda client the collector uses, matching an IAM policy
// of lambda:ListFunctions and lambda:GetFunction.
type API interface {
	ListFunctions(ctx context.Context, in *lambda.ListFunctionsInput, opts ...func(*lambda.Options)) (*lambda.ListFunctionsOutput, error)
	GetFunction(ctx context.Context, in *lambda.GetFunctionInput, opts ...func(*lambda.Options)) (*lambda.GetFunctionOutput, error)
}

// New is the collectors.Factory for lambda targets. It uses the default AWS credential
// chain; credentials_ref, when set, names a profile in the shared AWS config.
func New(ctx context.Context, t agentproto.Target) (collectors.Collector, error) {
	if t.Lambda == nil || t.Lambda.Region == "" {
		return nil, errors.New("lambda target has no region")
	}
	opts := []func(*awsconfig.LoadOptions) error{awsconfig.WithRegion(t.Lambda.Region)}
	if t.CredentialsRef != nil && *t.CredentialsRef != "" {
		opts = append(opts, awsconfig.WithSharedConfigProfile(*t.CredentialsRef))
	}
	cfg, err := awsconfig.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("aws config: %w", err)
	}
	return NewWithAPI(lambda.NewFromConfig(cfg), t.Lambda.Region, t.Lambda.NamePrefixes), nil
}

// NewWithAPI returns a collector using api for the functions of region whose names
// start with one of prefixes (all when empty).
func NewWithAPI(api API, region string, prefixes []string) *Collector {
	return &Collector{api: api, region: region, prefixes: prefixes}
}

// Collector reads Lambda functions: the image of container functions, the runtime of
// .zip functions, and their tags.
type Collector struct {
	api      API
	region   string
	prefixes []string
}

// parallel is how many GetFunction calls run at once, well below Lambda's control
// plane limits.
const parallel = 4

// Collect lists the functions, then reads each one's code location and tags. A
// function that cannot be read marks the result incomplete; the others are reported.
func (c *Collector) Collect(ctx context.Context) (collectors.Result, error) {
	var fns []types.FunctionConfiguration
	var marker *string
	for {
		out, err := c.api.ListFunctions(ctx, &lambda.ListFunctionsInput{Marker: marker})
		if err != nil {
			return collectors.Result{}, fmt.Errorf("list functions: %w", err)
		}
		for _, f := range out.Functions {
			if c.wanted(aws.ToString(f.FunctionName)) {
				fns = append(fns, f)
			}
		}
		if marker = out.NextMarker; marker == nil {
			break
		}
	}

	res := collectors.Result{Complete: true, Workloads: make([]agentproto.Workload, len(fns))}
	errs := make([]error, len(fns))
	sem := make(chan struct{}, parallel)
	var wg sync.WaitGroup
	for i, f := range fns {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer func() { <-sem; wg.Done() }()
			res.Workloads[i], errs[i] = c.function(ctx, f)
		}()
	}
	wg.Wait()
	kept := res.Workloads[:0]
	for i, w := range res.Workloads {
		if errs[i] != nil {
			res.Complete = false
			res.Errors = append(res.Errors, fmt.Sprintf("function %s: %v", aws.ToString(fns[i].FunctionName), errs[i]))
			continue
		}
		kept = append(kept, w)
	}
	res.Workloads = kept
	sort.Slice(res.Workloads, func(i, j int) bool { return res.Workloads[i].Name < res.Workloads[j].Name })
	return res, nil
}

func (c *Collector) wanted(name string) bool {
	if len(c.prefixes) == 0 {
		return true
	}
	for _, p := range c.prefixes {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

func (c *Collector) function(ctx context.Context, f types.FunctionConfiguration) (agentproto.Workload, error) {
	region := c.region
	w := agentproto.Workload{
		ID: aws.ToString(f.FunctionArn), Kind: agentproto.LambdaFunction, Namespace: &region,
		Name: aws.ToString(f.FunctionName), Containers: []agentproto.Container{},
	}
	out, err := c.api.GetFunction(ctx, &lambda.GetFunctionInput{FunctionName: f.FunctionName})
	if err != nil {
		return w, err
	}
	if len(out.Tags) > 0 {
		w.Labels = out.Tags
	}
	if f.PackageType == types.PackageTypeImage {
		ct := agentproto.Container{Name: "function", Running: 1}
		if out.Code != nil {
			ct.Image = aws.ToString(out.Code.ImageUri)
			if _, digest, ok := strings.Cut(aws.ToString(out.Code.ResolvedImageUri), "@"); ok && strings.HasPrefix(digest, "sha256:") {
				ct.Digest = &digest
			}
		}
		if ct.Image == "" {
			return w, errors.New("container function without an image URI")
		}
		w.Containers = append(w.Containers, ct)
		return w, nil
	}
	if image := RuntimeImage(string(f.Runtime)); image != "" {
		w.Containers = append(w.Containers, agentproto.Container{Name: "runtime", Image: image, Running: 1})
	}
	return w, nil
}

var runtimeShape = regexp.MustCompile(`^(python|nodejs|java|dotnet|ruby|go)(\d+(?:\.\d+)?)(?:\.x)?(\.al2(?:023)?)?$`)

// RuntimeImage is the AWS base image of a Lambda runtime: python3.12 is
// public.ecr.aws/lambda/python:3.12, nodejs20.x is public.ecr.aws/lambda/nodejs:20,
// java8.al2 is public.ecr.aws/lambda/java:8.al2 and provided.al2023 is
// public.ecr.aws/lambda/provided:al2023. Empty for runtimes without one
// (dotnetcore3.1, provided on Amazon Linux 1).
func RuntimeImage(runtime string) string {
	const base = "public.ecr.aws/lambda/"
	if tag, ok := strings.CutPrefix(runtime, "provided."); ok {
		return base + "provided:" + tag
	}
	m := runtimeShape.FindStringSubmatch(runtime)
	if m == nil {
		return ""
	}
	return base + m[1] + ":" + m[2] + m[3]
}
