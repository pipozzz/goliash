// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: Apache-2.0

// Package ecr lists image tags in private Amazon ECR repositories through the ECR
// API (ecr:ListImages, read-only), with the agent's AWS credentials.
package ecr

import (
	"context"
	"fmt"
	"regexp"
	"sync"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ecr"
	"github.com/aws/aws-sdk-go-v2/service/ecr/types"
)

// host is a private ECR registry: <account>.dkr.ecr[-fips].<region>.amazonaws.com[.cn].
var host = regexp.MustCompile(`^(\d{12})\.dkr\.ecr(?:-fips)?\.([a-z0-9-]+)\.amazonaws\.com(?:\.cn)?/(.+)$`)

// Parse splits an ECR image repository into account, region and repository name.
// ok is false for any other registry.
func Parse(repository string) (account, region, name string, ok bool) {
	m := host.FindStringSubmatch(repository)
	if m == nil {
		return "", "", "", false
	}
	return m[1], m[2], m[3], true
}

// API is the part of the ECR client the lister uses.
type API interface {
	ListImages(ctx context.Context, in *ecr.ListImagesInput, opts ...func(*ecr.Options)) (*ecr.ListImagesOutput, error)
}

// Lister lists tags of ECR repositories, with one client per region and AWS profile.
type Lister struct {
	// NewAPI builds a client; the default uses the AWS default credential chain.
	NewAPI func(ctx context.Context, region, profile string) (API, error)

	mu      sync.Mutex
	clients map[string]API
}

// New returns a Lister using the AWS default credential chain (environment, shared
// config, web identity, ECS task role, EC2 instance profile).
func New() *Lister {
	return &Lister{NewAPI: defaultAPI}
}

func defaultAPI(ctx context.Context, region, profile string) (API, error) {
	opts := []func(*awsconfig.LoadOptions) error{awsconfig.WithRegion(region)}
	if profile != "" {
		opts = append(opts, awsconfig.WithSharedConfigProfile(profile))
	}
	cfg, err := awsconfig.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("aws config: %w", err)
	}
	return ecr.NewFromConfig(cfg), nil
}

// maxPages bounds listing for repositories with very many images.
const maxPages = 50

// ListTags returns every tag of an ECR repository. profile names an AWS shared
// config profile; empty uses the default chain.
func (l *Lister) ListTags(ctx context.Context, repository, profile string) ([]string, error) {
	account, region, name, ok := Parse(repository)
	if !ok {
		return nil, fmt.Errorf("%s is not an ECR repository", repository)
	}
	api, err := l.client(ctx, region, profile)
	if err != nil {
		return nil, err
	}
	in := &ecr.ListImagesInput{
		RegistryId: aws.String(account), RepositoryName: aws.String(name), MaxResults: aws.Int32(1000),
		Filter: &types.ListImagesFilter{TagStatus: types.TagStatusTagged},
	}
	var tags []string
	seen := map[string]bool{}
	for page := 0; page < maxPages; page++ {
		out, err := api.ListImages(ctx, in)
		if err != nil {
			return nil, fmt.Errorf("ecr ListImages %s: %w", name, err)
		}
		for _, id := range out.ImageIds {
			if t := aws.ToString(id.ImageTag); t != "" && !seen[t] {
				seen[t] = true
				tags = append(tags, t)
			}
		}
		if out.NextToken == nil {
			break
		}
		in.NextToken = out.NextToken
	}
	return tags, nil
}

func (l *Lister) client(ctx context.Context, region, profile string) (API, error) {
	key := region + "|" + profile
	l.mu.Lock()
	defer l.mu.Unlock()
	if c, ok := l.clients[key]; ok {
		return c, nil
	}
	c, err := l.NewAPI(ctx, region, profile)
	if err != nil {
		return nil, err
	}
	if l.clients == nil {
		l.clients = map[string]API{}
	}
	l.clients[key] = c
	return c, nil
}
