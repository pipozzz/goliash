// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: Apache-2.0

// Package msk collects the Amazon MSK (Managed Streaming for Apache Kafka) clusters of a region, using
// kafka:ListClustersV2 only, and lists the Kafka versions MSK offers (kafka:ListKafkaVersions).
package msk

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/kafka"
	"github.com/aws/aws-sdk-go-v2/service/kafka/types"

	"github.com/pipozzz/goliash/internal/collectors"
	"github.com/pipozzz/goliash/pkg/agentproto"
)

// Image is the repository of the image clusters report: kafka.amazonaws.com/kafka:3.6.0. It names no real
// registry; the agent answers for it with the Kafka versions MSK offers.
const Image = "kafka.amazonaws.com/kafka"

// API is the part of the MSK client the collector uses.
type API interface {
	ListClustersV2(ctx context.Context, in *kafka.ListClustersV2Input, opts ...func(*kafka.Options)) (*kafka.ListClustersV2Output, error)
	ListKafkaVersions(ctx context.Context, in *kafka.ListKafkaVersionsInput, opts ...func(*kafka.Options)) (*kafka.ListKafkaVersionsOutput, error)
}

// New is the collectors.Factory for msk targets. It uses the default AWS credential chain; credentials_ref, when
// set, names a profile in the shared AWS config.
func New(ctx context.Context, t agentproto.Target) (collectors.Collector, error) {
	if t.Msk == nil || t.Msk.Region == "" {
		return nil, errors.New("msk target has no region")
	}
	profile := ""
	if t.CredentialsRef != nil {
		profile = *t.CredentialsRef
	}
	api, err := Client(ctx, t.Msk.Region, profile)
	if err != nil {
		return nil, err
	}
	return NewWithAPI(api, t.Msk.Region), nil
}

// Client is an MSK client for region, with the default credential chain or a profile of the shared AWS config.
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
	return kafka.NewFromConfig(cfg), nil
}

// NewWithAPI returns a collector using api for the clusters of region.
func NewWithAPI(api API, region string) *Collector { return &Collector{api: api, region: region} }

// Collector reads the provisioned MSK clusters of a region; serverless clusters have no Kafka version to keep up.
type Collector struct {
	api    API
	region string
}

// Collect lists the region's clusters.
func (c *Collector) Collect(ctx context.Context) (collectors.Result, error) {
	res := collectors.Result{Complete: true, Workloads: []agentproto.Workload{}}
	var token *string
	for {
		out, err := c.api.ListClustersV2(ctx, &kafka.ListClustersV2Input{NextToken: token})
		if err != nil {
			return res, fmt.Errorf("list clusters: %w", err)
		}
		for _, cl := range out.ClusterInfoList {
			if w, ok := c.cluster(cl); ok {
				res.Workloads = append(res.Workloads, w)
			}
		}
		if token = out.NextToken; token == nil {
			break
		}
	}
	sort.Slice(res.Workloads, func(i, j int) bool { return res.Workloads[i].ID < res.Workloads[j].ID })
	return res, nil
}

func (c *Collector) cluster(cl types.Cluster) (agentproto.Workload, bool) {
	p := cl.Provisioned
	if p == nil || p.CurrentBrokerSoftwareInfo == nil || aws.ToString(p.CurrentBrokerSoftwareInfo.KafkaVersion) == "" ||
		cl.State == types.ClusterStateDeleting {
		return agentproto.Workload{}, false
	}
	region := c.region
	brokers := int(aws.ToInt32(p.NumberOfBrokerNodes))
	w := agentproto.Workload{
		ID: aws.ToString(cl.ClusterArn), Kind: agentproto.MskCluster, Namespace: &region, Name: aws.ToString(cl.ClusterName),
		DesiredReplicas: &brokers,
		Containers:      []agentproto.Container{{Name: "kafka", Image: Image + ":" + Version(aws.ToString(p.CurrentBrokerSoftwareInfo.KafkaVersion)), Running: brokers}},
	}
	if len(cl.Tags) > 0 {
		w.Labels = cl.Tags
	}
	if b := p.BrokerNodeGroupInfo; b != nil && b.ConnectivityInfo != nil && b.ConnectivityInfo.PublicAccess != nil &&
		aws.ToString(b.ConnectivityInfo.PublicAccess.Type) == "SERVICE_PROVIDED_EIPS" {
		w.Exposed = []string{"public brokers"}
	}
	return w, true
}

// Versions lists the Kafka versions MSK offers (active ones), as tags of Image.
func Versions(ctx context.Context, api API, repo string) ([]string, error) {
	if repo != Image {
		return nil, fmt.Errorf("%s: not MSK", repo)
	}
	var out []string
	var token *string
	for {
		page, err := api.ListKafkaVersions(ctx, &kafka.ListKafkaVersionsInput{NextToken: token})
		if err != nil {
			return nil, err
		}
		for _, v := range page.KafkaVersions {
			if s := Version(aws.ToString(v.Version)); s != "" && v.Status == types.KafkaVersionStatusActive && !slices.Contains(out, s) {
				out = append(out, s)
			}
		}
		if token = page.NextToken; token == nil {
			return out, nil
		}
	}
}

// Version is a Kafka version as Goliash compares it: MSK's 3.7.x (the latest patch, which MSK applies itself) is
// 3.7, and its KRaft and tiered-storage variants (3.7.x.kraft, 2.8.2.tiered) are the Kafka version they run.
func Version(v string) string {
	v = strings.TrimSuffix(strings.TrimSuffix(v, ".kraft"), ".tiered")
	return strings.TrimSuffix(v, ".x")
}
