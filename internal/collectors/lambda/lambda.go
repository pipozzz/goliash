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
	"sync/atomic"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	"github.com/aws/aws-sdk-go-v2/service/lambda/types"

	"github.com/pipozzz/goliash/internal/collectors"
	"github.com/pipozzz/goliash/pkg/agentproto"
)

// API is the subset of the Lambda client the collector uses, matching an IAM policy
// of lambda:ListFunctions, lambda:GetFunction and lambda:ListAliases.
type API interface {
	ListFunctions(ctx context.Context, in *lambda.ListFunctionsInput, opts ...func(*lambda.Options)) (*lambda.ListFunctionsOutput, error)
	GetFunction(ctx context.Context, in *lambda.GetFunctionInput, opts ...func(*lambda.Options)) (*lambda.GetFunctionOutput, error)
	ListAliases(ctx context.Context, in *lambda.ListAliasesInput, opts ...func(*lambda.Options)) (*lambda.ListAliasesOutput, error)
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
	c := NewWithAPI(lambda.NewFromConfig(cfg), t.Lambda.Region, t.Lambda.NamePrefixes)
	c.aliasEnvs = t.Lambda.AliasEnvironments
	return c, nil
}

// NewWithAPI returns a collector using api for the functions of region whose names
// start with one of prefixes (all when empty).
func NewWithAPI(api API, region string, prefixes []string) *Collector {
	return &Collector{api: api, region: region, prefixes: prefixes}
}

// Collector reads Lambda functions: the image of container functions, the runtime of
// .zip functions, and their tags. A function with aliases is reported once per
// alias, with the version the alias points to, in the environment named like the
// alias (or as aliasEnvs maps it; "-" leaves an alias out); one without is reported
// as its latest code.
type Collector struct {
	api       API
	region    string
	prefixes  []string
	aliasEnvs map[string]string

	noAliases atomic.Bool // lambda:ListAliases was denied: report latest code only
}

// aliasLabel names the alias a workload stands for.
const aliasLabel = "goliash.lambda/alias"

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

	res := collectors.Result{Complete: true, Workloads: []agentproto.Workload{}}
	found := make([][]agentproto.Workload, len(fns))
	errs := make([]error, len(fns))
	sem := make(chan struct{}, parallel)
	var wg sync.WaitGroup
	for i, f := range fns {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer func() { <-sem; wg.Done() }()
			found[i], errs[i] = c.function(ctx, f)
		}()
	}
	wg.Wait()
	for i, ws := range found {
		if errs[i] != nil {
			res.Complete = false
			res.Errors = append(res.Errors, fmt.Sprintf("function %s: %v", aws.ToString(fns[i].FunctionName), errs[i]))
			continue
		}
		res.Workloads = append(res.Workloads, ws...)
	}
	if c.noAliases.Load() {
		res.Errors = append(res.Errors, "aliases not read (lambda:ListAliases denied): reporting each function's latest code")
	}
	sort.SliceStable(res.Workloads, func(i, j int) bool { return res.Workloads[i].ID < res.Workloads[j].ID })
	sort.SliceStable(res.Workloads, func(i, j int) bool { return res.Workloads[i].Name < res.Workloads[j].Name })
	return res, nil
}

// aliases lists a function's aliases; nil when it has none, or when the agent may
// not list them (then never again in this collector's life).
func (c *Collector) aliases(ctx context.Context, name *string) ([]types.AliasConfiguration, error) {
	if c.noAliases.Load() {
		return nil, nil
	}
	var out []types.AliasConfiguration
	var marker *string
	for {
		page, err := c.api.ListAliases(ctx, &lambda.ListAliasesInput{FunctionName: name, Marker: marker})
		if err != nil {
			if strings.Contains(err.Error(), "AccessDenied") {
				c.noAliases.Store(true)
				return nil, nil
			}
			return nil, fmt.Errorf("list aliases: %w", err)
		}
		out = append(out, page.Aliases...)
		if marker = page.NextMarker; marker == nil {
			return out, nil
		}
	}
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

// function reads one function: its latest code, or the version behind each alias.
func (c *Collector) function(ctx context.Context, f types.FunctionConfiguration) ([]agentproto.Workload, error) {
	region := c.region
	latest, err := c.api.GetFunction(ctx, &lambda.GetFunctionInput{FunctionName: f.FunctionName})
	if err != nil {
		return nil, err
	}
	aliases, err := c.aliases(ctx, f.FunctionName)
	if err != nil {
		return nil, err
	}
	workload := func(id string, labels map[string]string, out *lambda.GetFunctionOutput) (agentproto.Workload, error) {
		w := agentproto.Workload{
			ID: id, Kind: agentproto.LambdaFunction, Namespace: &region,
			Name: aws.ToString(f.FunctionName), Containers: []agentproto.Container{},
		}
		if len(labels) > 0 {
			w.Labels = labels
		}
		ct, err := code(f, out)
		if err != nil {
			return w, err
		}
		if ct != nil {
			w.Containers = append(w.Containers, *ct)
		}
		return w, nil
	}
	if len(aliases) == 0 {
		w, err := workload(aws.ToString(f.FunctionArn), latest.Tags, latest)
		return []agentproto.Workload{w}, err
	}
	versions := map[string]*lambda.GetFunctionOutput{"$LATEST": latest}
	var out []agentproto.Workload
	for _, a := range aliases {
		if c.aliasEnvs[aws.ToString(a.Name)] == "-" {
			continue // left out on purpose, e.g. a canary
		}
		v := aws.ToString(a.FunctionVersion)
		got, ok := versions[v]
		if !ok {
			if got, err = c.api.GetFunction(ctx, &lambda.GetFunctionInput{FunctionName: f.FunctionName, Qualifier: &v}); err != nil {
				return nil, fmt.Errorf("version %s: %w", v, err)
			}
			versions[v] = got
		}
		alias := aws.ToString(a.Name)
		labels := map[string]string{aliasLabel: alias}
		for k, val := range latest.Tags {
			labels[k] = val
		}
		if _, set := latest.Tags["goliash.env"]; !set {
			labels["goliash.env"] = alias
			if env := c.aliasEnvs[alias]; env != "" {
				labels["goliash.env"] = env
			}
		}
		w, err := workload(aws.ToString(a.AliasArn), labels, got)
		if err != nil {
			return nil, fmt.Errorf("alias %s: %w", alias, err)
		}
		out = append(out, w)
	}
	return out, nil
}

// code is what a function version runs: its image, or its runtime's base image. The
// version's own configuration wins over the function's latest one.
func code(f types.FunctionConfiguration, out *lambda.GetFunctionOutput) (*agentproto.Container, error) {
	pkg, runtime := f.PackageType, f.Runtime
	if cfg := out.Configuration; cfg != nil {
		if cfg.PackageType != "" {
			pkg = cfg.PackageType
		}
		if cfg.Runtime != "" {
			runtime = cfg.Runtime
		}
	}
	if pkg == types.PackageTypeImage {
		ct := agentproto.Container{Name: "function", Running: 1}
		if out.Code != nil {
			ct.Image = aws.ToString(out.Code.ImageUri)
			if _, digest, ok := strings.Cut(aws.ToString(out.Code.ResolvedImageUri), "@"); ok && strings.HasPrefix(digest, "sha256:") {
				ct.Digest = &digest
			}
		}
		if ct.Image == "" {
			return nil, errors.New("container function without an image URI")
		}
		return &ct, nil
	}
	if image := RuntimeImage(string(runtime)); image != "" {
		return &agentproto.Container{Name: "runtime", Image: image, Running: 1}, nil
	}
	return nil, nil
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
