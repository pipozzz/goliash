// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: Apache-2.0

package elasticache

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/elasticache"
	"github.com/aws/aws-sdk-go-v2/service/elasticache/types"

	"github.com/pipozzz/goliash/pkg/agentproto"
)

type fakeAPI struct {
	clusters   []types.CacheCluster
	groups     []types.ReplicationGroup
	serverless []types.ServerlessCache
	tags       map[string][]types.Tag
	versions   map[string][]string
	tagCalls   int
}

func (f *fakeAPI) DescribeCacheClusters(context.Context, *elasticache.DescribeCacheClustersInput, ...func(*elasticache.Options)) (*elasticache.DescribeCacheClustersOutput, error) {
	return &elasticache.DescribeCacheClustersOutput{CacheClusters: f.clusters}, nil
}

func (f *fakeAPI) DescribeReplicationGroups(context.Context, *elasticache.DescribeReplicationGroupsInput, ...func(*elasticache.Options)) (*elasticache.DescribeReplicationGroupsOutput, error) {
	return &elasticache.DescribeReplicationGroupsOutput{ReplicationGroups: f.groups}, nil
}

func (f *fakeAPI) DescribeServerlessCaches(context.Context, *elasticache.DescribeServerlessCachesInput, ...func(*elasticache.Options)) (*elasticache.DescribeServerlessCachesOutput, error) {
	return &elasticache.DescribeServerlessCachesOutput{ServerlessCaches: f.serverless}, nil
}

func (f *fakeAPI) DescribeCacheEngineVersions(_ context.Context, in *elasticache.DescribeCacheEngineVersionsInput, _ ...func(*elasticache.Options)) (*elasticache.DescribeCacheEngineVersionsOutput, error) {
	out := &elasticache.DescribeCacheEngineVersionsOutput{}
	for _, v := range f.versions[aws.ToString(in.Engine)] {
		out.CacheEngineVersions = append(out.CacheEngineVersions, types.CacheEngineVersion{EngineVersion: aws.String(v)})
	}
	return out, nil
}

func (f *fakeAPI) ListTagsForResource(_ context.Context, in *elasticache.ListTagsForResourceInput, _ ...func(*elasticache.Options)) (*elasticache.ListTagsForResourceOutput, error) {
	f.tagCalls++
	if f.tags == nil {
		return nil, errors.New("AccessDenied")
	}
	return &elasticache.ListTagsForResourceOutput{TagList: f.tags[aws.ToString(in.ResourceName)]}, nil
}

func cluster(id, engine, version, group string, nodes int32) types.CacheCluster {
	c := types.CacheCluster{
		CacheClusterId: aws.String(id), ARN: aws.String("arn:aws:elasticache:eu-west-1:1:cluster:" + id), Engine: aws.String(engine),
		EngineVersion: aws.String(version), CacheClusterStatus: aws.String("available"), NumCacheNodes: aws.Int32(nodes),
	}
	if group != "" {
		c.ReplicationGroupId = aws.String(group)
	}
	return c
}

func TestCollect(t *testing.T) {
	sessions := "arn:aws:elasticache:eu-west-1:1:replicationgroup:sessions"
	api := &fakeAPI{
		// A replication group halfway through an upgrade, a standalone Memcached, and an engine left out.
		clusters: []types.CacheCluster{
			cluster("sessions-001", "redis", "6.2.6", "sessions", 1), cluster("sessions-002", "redis", "7.1.0", "sessions", 1),
			cluster("sessions-003", "redis", "6.2.6", "sessions", 1), cluster("pages", "memcached", "1.6.22", "", 3),
			cluster("other", "unknown", "1.0", "", 1),
		},
		groups: []types.ReplicationGroup{{ReplicationGroupId: aws.String("sessions"), ARN: aws.String(sessions)}},
		serverless: []types.ServerlessCache{{
			ServerlessCacheName: aws.String("rates"), ARN: aws.String("arn:aws:elasticache:eu-west-1:1:serverlesscache:rates"),
			Engine: aws.String("valkey"), FullEngineVersion: aws.String("8.0"), MajorEngineVersion: aws.String("8"), Status: aws.String("available"),
		}},
		tags: map[string][]types.Tag{sessions: {{Key: aws.String("goliash.env"), Value: aws.String("prod")}}},
	}
	res, err := NewWithAPI(api, "eu-west-1").Collect(context.Background())
	if err != nil || !res.Complete || len(res.Workloads) != 3 {
		t.Fatalf("collect: %+v %v", res, err)
	}
	got := map[string]agentproto.Workload{}
	for _, w := range res.Workloads {
		got[w.Name] = w
	}
	s := got["sessions"]
	if s.Kind != agentproto.ElasticacheCluster || len(s.Containers) != 2 || s.Containers[0].Image != "elasticache.amazonaws.com/redis:6.2.6" ||
		s.Containers[0].Running != 2 || s.Containers[1].Running != 1 || s.Labels["goliash.env"] != "prod" {
		t.Errorf("sessions %+v", s)
	}
	if p := got["pages"]; p.Containers[0].Image != "elasticache.amazonaws.com/memcached:1.6.22" || p.Containers[0].Running != 3 {
		t.Errorf("pages %+v", p)
	}
	if r := got["rates"]; r.Kind != agentproto.ElasticacheServerless || r.Containers[0].Image != "elasticache.amazonaws.com/valkey:8.0" {
		t.Errorf("rates %+v", r)
	}

	// Without elasticache:ListTagsForResource the caches still count, and the agent stops asking.
	api.tags, api.tagCalls = nil, 0
	res, _ = NewWithAPI(api, "eu-west-1").Collect(context.Background())
	if len(res.Workloads) != 3 || api.tagCalls != 1 {
		t.Errorf("no tags: %d workloads, %d tag calls", len(res.Workloads), api.tagCalls)
	}
}

func TestEngineVersions(t *testing.T) {
	api := &fakeAPI{versions: map[string][]string{"redis": {"6.2.6", "7.1.0", "7.1.0"}}}
	if got, err := EngineVersions(context.Background(), api, "elasticache.amazonaws.com/redis"); err != nil || !slices.Equal(got, []string{"6.2.6", "7.1.0"}) {
		t.Errorf("redis: %v %v", got, err)
	}
	if _, err := EngineVersions(context.Background(), api, "elasticache.amazonaws.com/other"); err == nil {
		t.Error("an unknown engine was listed")
	}
}
