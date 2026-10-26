// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: Apache-2.0

// Package elasticache collects the Amazon ElastiCache caches of a region (Redis OSS, Valkey, Memcached), using
// elasticache:Describe* and elasticache:ListTagsForResource only, and lists the engine versions ElastiCache offers.
package elasticache

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync/atomic"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/elasticache"

	"github.com/pipozzz/goliash/internal/collectors"
	"github.com/pipozzz/goliash/pkg/agentproto"
)

// Host is the registry host of the images caches report: elasticache.amazonaws.com/redis:7.1.0. It names no real
// registry; the agent answers for it with the engine versions ElastiCache offers.
const Host = "elasticache.amazonaws.com"

var engines = map[string]bool{"redis": true, "valkey": true, "memcached": true}

// API is the part of the ElastiCache client the collector uses.
type API interface {
	DescribeCacheClusters(ctx context.Context, in *elasticache.DescribeCacheClustersInput, opts ...func(*elasticache.Options)) (*elasticache.DescribeCacheClustersOutput, error)
	DescribeReplicationGroups(ctx context.Context, in *elasticache.DescribeReplicationGroupsInput, opts ...func(*elasticache.Options)) (*elasticache.DescribeReplicationGroupsOutput, error)
	DescribeServerlessCaches(ctx context.Context, in *elasticache.DescribeServerlessCachesInput, opts ...func(*elasticache.Options)) (*elasticache.DescribeServerlessCachesOutput, error)
	DescribeCacheEngineVersions(ctx context.Context, in *elasticache.DescribeCacheEngineVersionsInput, opts ...func(*elasticache.Options)) (*elasticache.DescribeCacheEngineVersionsOutput, error)
	ListTagsForResource(ctx context.Context, in *elasticache.ListTagsForResourceInput, opts ...func(*elasticache.Options)) (*elasticache.ListTagsForResourceOutput, error)
}

// New is the collectors.Factory for elasticache targets. It uses the default AWS credential chain; credentials_ref,
// when set, names a profile in the shared AWS config.
func New(ctx context.Context, t agentproto.Target) (collectors.Collector, error) {
	if t.Elasticache == nil || t.Elasticache.Region == "" {
		return nil, errors.New("elasticache target has no region")
	}
	profile := ""
	if t.CredentialsRef != nil {
		profile = *t.CredentialsRef
	}
	api, err := Client(ctx, t.Elasticache.Region, profile)
	if err != nil {
		return nil, err
	}
	return NewWithAPI(api, t.Elasticache.Region), nil
}

// Client is an ElastiCache client for region, with the default credential chain or a profile of the shared AWS
// config.
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
	return elasticache.NewFromConfig(cfg), nil
}

// NewWithAPI returns a collector using api for the caches of region.
func NewWithAPI(api API, region string) *Collector { return &Collector{api: api, region: region} }

// Collector reads the caches of a region: each replication group once with its nodes, each standalone cluster, and
// each serverless cache.
type Collector struct {
	api    API
	region string

	noTags atomic.Bool // elasticache:ListTagsForResource was denied
}

type node struct{ engine, version string }

// Collect lists the region's caches.
func (c *Collector) Collect(ctx context.Context) (collectors.Result, error) {
	res := collectors.Result{Complete: true, Workloads: []agentproto.Workload{}}
	groups := map[string][]node{} // replication group -> its nodes
	var marker *string
	for {
		out, err := c.api.DescribeCacheClusters(ctx, &elasticache.DescribeCacheClustersInput{Marker: marker})
		if err != nil {
			return res, fmt.Errorf("describe cache clusters: %w", err)
		}
		for _, cl := range out.CacheClusters {
			engine := aws.ToString(cl.Engine)
			if !engines[engine] || aws.ToString(cl.CacheClusterStatus) == "deleting" {
				continue
			}
			n := node{engine, aws.ToString(cl.EngineVersion)}
			if g := aws.ToString(cl.ReplicationGroupId); g != "" {
				groups[g] = append(groups[g], n)
				continue
			}
			running := int(aws.ToInt32(cl.NumCacheNodes))
			res.Workloads = append(res.Workloads, c.workload(ctx, aws.ToString(cl.ARN), agentproto.ElasticacheCluster,
				aws.ToString(cl.CacheClusterId), map[node]int{n: running}))
		}
		if marker = out.Marker; marker == nil {
			break
		}
	}
	if len(groups) > 0 {
		marker = nil
		for {
			out, err := c.api.DescribeReplicationGroups(ctx, &elasticache.DescribeReplicationGroupsInput{Marker: marker})
			if err != nil {
				res.Complete, res.Errors = false, append(res.Errors, "describe replication groups: "+err.Error())
				break
			}
			for _, g := range out.ReplicationGroups {
				nodes := groups[aws.ToString(g.ReplicationGroupId)]
				if len(nodes) == 0 {
					continue
				}
				counts := map[node]int{}
				for _, n := range nodes {
					counts[n]++
				}
				res.Workloads = append(res.Workloads, c.workload(ctx, aws.ToString(g.ARN), agentproto.ElasticacheCluster,
					aws.ToString(g.ReplicationGroupId), counts))
			}
			if marker = out.Marker; marker == nil {
				break
			}
		}
	}
	var token *string
	for {
		out, err := c.api.DescribeServerlessCaches(ctx, &elasticache.DescribeServerlessCachesInput{NextToken: token})
		if err != nil {
			res.Complete, res.Errors = false, append(res.Errors, "describe serverless caches: "+err.Error())
			break
		}
		for _, s := range out.ServerlessCaches {
			engine := aws.ToString(s.Engine)
			if !engines[engine] || aws.ToString(s.Status) == "deleting" {
				continue
			}
			version := aws.ToString(s.FullEngineVersion)
			if version == "" {
				version = aws.ToString(s.MajorEngineVersion)
			}
			res.Workloads = append(res.Workloads, c.workload(ctx, aws.ToString(s.ARN), agentproto.ElasticacheServerless,
				aws.ToString(s.ServerlessCacheName), map[node]int{{engine, version}: 1}))
		}
		if token = out.NextToken; token == nil {
			break
		}
	}
	sort.Slice(res.Workloads, func(i, j int) bool { return res.Workloads[i].ID < res.Workloads[j].ID })
	return res, nil
}

// workload is one cache, with a container per engine version its nodes run (two while an upgrade rolls).
func (c *Collector) workload(ctx context.Context, arn string, kind agentproto.WorkloadKind, name string, nodes map[node]int) agentproto.Workload {
	region := c.region
	w := agentproto.Workload{ID: arn, Kind: kind, Namespace: &region, Name: name, Containers: []agentproto.Container{}}
	for n, count := range nodes {
		w.Containers = append(w.Containers, agentproto.Container{Name: n.engine, Image: Host + "/" + n.engine + ":" + n.version, Running: count})
	}
	sort.Slice(w.Containers, func(i, j int) bool { return w.Containers[i].Image < w.Containers[j].Image })
	if c.noTags.Load() || arn == "" {
		return w
	}
	out, err := c.api.ListTagsForResource(ctx, &elasticache.ListTagsForResourceInput{ResourceName: &arn})
	if err != nil {
		c.noTags.Store(true) // without the permission, caches still count; goliash.ignore and goliash.env do not
		return w
	}
	if len(out.TagList) > 0 {
		w.Labels = map[string]string{}
		for _, t := range out.TagList {
			w.Labels[aws.ToString(t.Key)] = aws.ToString(t.Value)
		}
	}
	return w
}

// EngineVersions lists the versions ElastiCache offers of an engine, the repository
// elasticache.amazonaws.com/<engine>, as tags.
func EngineVersions(ctx context.Context, api API, repo string) ([]string, error) {
	engine, ok := strings.CutPrefix(repo, Host+"/")
	if !ok || !engines[engine] {
		return nil, fmt.Errorf("%s: not an ElastiCache engine", repo)
	}
	var out []string
	seen := map[string]bool{}
	var marker *string
	for {
		page, err := api.DescribeCacheEngineVersions(ctx, &elasticache.DescribeCacheEngineVersionsInput{Engine: &engine, Marker: marker})
		if err != nil {
			return nil, err
		}
		for _, v := range page.CacheEngineVersions {
			if t := aws.ToString(v.EngineVersion); t != "" && !seen[t] {
				seen[t] = true
				out = append(out, t)
			}
		}
		if marker = page.Marker; marker == nil {
			return out, nil
		}
	}
}
