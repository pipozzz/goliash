// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: Apache-2.0

// Package opensearch collects the Amazon OpenSearch Service domains of a region (OpenSearch and legacy
// Elasticsearch), using es:ListDomainNames, es:DescribeDomains and es:ListTags only, and lists the engine versions
// the service offers (es:ListVersions).
package opensearch

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync/atomic"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/opensearch"
	"github.com/aws/aws-sdk-go-v2/service/opensearch/types"

	"github.com/pipozzz/goliash/internal/collectors"
	"github.com/pipozzz/goliash/pkg/agentproto"
)

// Host is the registry host of the images domains report: opensearch.amazonaws.com/opensearch:2.13 or
// opensearch.amazonaws.com/elasticsearch:7.10. It names no real registry; the agent answers for it with the engine
// versions the service offers.
const Host = "opensearch.amazonaws.com"

// UpdateLabel holds the service software update a domain has waiting (OpenSearch_2_13_R20250101-P1), when AWS
// offers one: patches, often security fixes, applied by the next update.
const UpdateLabel = "goliash.aws/service-software-update"

// API is the part of the OpenSearch Service client the collector uses.
type API interface {
	ListDomainNames(ctx context.Context, in *opensearch.ListDomainNamesInput, opts ...func(*opensearch.Options)) (*opensearch.ListDomainNamesOutput, error)
	DescribeDomains(ctx context.Context, in *opensearch.DescribeDomainsInput, opts ...func(*opensearch.Options)) (*opensearch.DescribeDomainsOutput, error)
	ListTags(ctx context.Context, in *opensearch.ListTagsInput, opts ...func(*opensearch.Options)) (*opensearch.ListTagsOutput, error)
	ListVersions(ctx context.Context, in *opensearch.ListVersionsInput, opts ...func(*opensearch.Options)) (*opensearch.ListVersionsOutput, error)
}

// New is the collectors.Factory for opensearch targets. It uses the default AWS credential chain; credentials_ref,
// when set, names a profile in the shared AWS config.
func New(ctx context.Context, t agentproto.Target) (collectors.Collector, error) {
	if t.Opensearch == nil || t.Opensearch.Region == "" {
		return nil, errors.New("opensearch target has no region")
	}
	profile := ""
	if t.CredentialsRef != nil {
		profile = *t.CredentialsRef
	}
	api, err := Client(ctx, t.Opensearch.Region, profile)
	if err != nil {
		return nil, err
	}
	return NewWithAPI(api, t.Opensearch.Region), nil
}

// Client is an OpenSearch Service client for region, with the default credential chain or a profile of the shared
// AWS config.
func Client(ctx context.Context, region, profile string) (API, error) {
	var opts []func(*awsconfig.LoadOptions) error
	if region != "" {
		opts = append(opts, awsconfig.WithRegion(region))
	}
	if profile != "" {
		opts = append(opts, awsconfig.WithSharedConfigProfile(profile))
	}
	cfg, err := awsconfig.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("aws config: %w", err)
	}
	return opensearch.NewFromConfig(cfg), nil
}

// NewWithAPI returns a collector using api for the domains of region.
func NewWithAPI(api API, region string) *Collector { return &Collector{api: api, region: region} }

// Collector reads the OpenSearch Service domains of a region.
type Collector struct {
	api    API
	region string

	noTags atomic.Bool // es:ListTags was denied
}

// Collect lists the region's domains.
func (c *Collector) Collect(ctx context.Context) (collectors.Result, error) {
	res := collectors.Result{Complete: true, Workloads: []agentproto.Workload{}}
	out, err := c.api.ListDomainNames(ctx, &opensearch.ListDomainNamesInput{})
	if err != nil {
		return res, fmt.Errorf("list domain names: %w", err)
	}
	var names []string
	for _, d := range out.DomainNames {
		names = append(names, aws.ToString(d.DomainName))
	}
	for batch := range slices.Chunk(names, 5) { // DescribeDomains takes at most 5
		d, err := c.api.DescribeDomains(ctx, &opensearch.DescribeDomainsInput{DomainNames: batch})
		if err != nil {
			res.Complete, res.Errors = false, append(res.Errors, "describe domains: "+err.Error())
			continue
		}
		for _, s := range d.DomainStatusList {
			if w, ok := c.domain(ctx, s); ok {
				res.Workloads = append(res.Workloads, w)
			}
		}
	}
	sort.Slice(res.Workloads, func(i, j int) bool { return res.Workloads[i].ID < res.Workloads[j].ID })
	return res, nil
}

func (c *Collector) domain(ctx context.Context, s types.DomainStatus) (agentproto.Workload, bool) {
	engine, version, ok := EngineVersion(aws.ToString(s.EngineVersion))
	if !ok || aws.ToBool(s.Deleted) {
		return agentproto.Workload{}, false
	}
	region := c.region
	nodes := 1
	if s.ClusterConfig != nil && s.ClusterConfig.InstanceCount != nil {
		nodes = int(aws.ToInt32(s.ClusterConfig.InstanceCount))
	}
	arn := aws.ToString(s.ARN)
	w := agentproto.Workload{
		ID: arn, Kind: agentproto.OpensearchDomain, Namespace: &region, Name: aws.ToString(s.DomainName), DesiredReplicas: &nodes,
		Containers: []agentproto.Container{{Name: engine, Image: Host + "/" + engine + ":" + version, Running: nodes}},
	}
	if s.VPCOptions == nil && aws.ToString(s.Endpoint) != "" {
		w.Exposed = []string{"public endpoint " + aws.ToString(s.Endpoint)}
	}
	labels := map[string]string{}
	if u := s.ServiceSoftwareOptions; u != nil && aws.ToBool(u.UpdateAvailable) && aws.ToString(u.NewVersion) != "" {
		labels[UpdateLabel] = aws.ToString(u.NewVersion)
	}
	if !c.noTags.Load() && arn != "" {
		if t, err := c.api.ListTags(ctx, &opensearch.ListTagsInput{ARN: &arn}); err != nil {
			c.noTags.Store(true) // without the permission, domains still count; goliash.ignore and goliash.env do not
		} else {
			for _, tag := range t.TagList {
				labels[aws.ToString(tag.Key)] = aws.ToString(tag.Value)
			}
		}
	}
	if len(labels) > 0 {
		w.Labels = labels
	}
	return w, true
}

// EngineVersion splits the service's engine version (OpenSearch_2.13, Elasticsearch_7.10) into the engine and its
// version.
func EngineVersion(v string) (engine, version string, ok bool) {
	name, version, ok := strings.Cut(v, "_")
	engine = strings.ToLower(name)
	if !ok || version == "" || (engine != "opensearch" && engine != "elasticsearch") {
		return "", "", false
	}
	return engine, version, true
}

// Versions lists the versions the service offers of an engine, the repository opensearch.amazonaws.com/<engine>,
// as tags.
func Versions(ctx context.Context, api API, repo string) ([]string, error) {
	want, ok := strings.CutPrefix(repo, Host+"/")
	if !ok || (want != "opensearch" && want != "elasticsearch") {
		return nil, fmt.Errorf("%s: not an OpenSearch Service engine", repo)
	}
	var out []string
	var token *string
	for {
		page, err := api.ListVersions(ctx, &opensearch.ListVersionsInput{NextToken: token})
		if err != nil {
			return nil, err
		}
		for _, v := range page.Versions {
			if engine, version, ok := EngineVersion(v); ok && engine == want && !slices.Contains(out, version) {
				out = append(out, version)
			}
		}
		if token = page.NextToken; token == nil {
			return out, nil
		}
	}
}
