// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: Apache-2.0

// Package eks collects the Amazon EKS clusters of a region, their control plane and managed node groups, using
// eks:List* and eks:Describe* calls only, and lists the Kubernetes versions EKS offers.
package eks

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	"github.com/aws/aws-sdk-go-v2/service/eks/types"

	"github.com/pipozzz/goliash/internal/collectors"
	"github.com/pipozzz/goliash/pkg/agentproto"
)

// Image is the repository of the image clusters and node groups report: eks.amazonaws.com/kubernetes:1.31. It
// names no real registry; the agent answers for it with the Kubernetes versions EKS offers.
const Image = "eks.amazonaws.com/kubernetes"

// API is the part of the EKS client the collector uses.
type API interface {
	ListClusters(ctx context.Context, in *eks.ListClustersInput, opts ...func(*eks.Options)) (*eks.ListClustersOutput, error)
	DescribeCluster(ctx context.Context, in *eks.DescribeClusterInput, opts ...func(*eks.Options)) (*eks.DescribeClusterOutput, error)
	ListNodegroups(ctx context.Context, in *eks.ListNodegroupsInput, opts ...func(*eks.Options)) (*eks.ListNodegroupsOutput, error)
	DescribeNodegroup(ctx context.Context, in *eks.DescribeNodegroupInput, opts ...func(*eks.Options)) (*eks.DescribeNodegroupOutput, error)
	DescribeClusterVersions(ctx context.Context, in *eks.DescribeClusterVersionsInput, opts ...func(*eks.Options)) (*eks.DescribeClusterVersionsOutput, error)
}

// New is the collectors.Factory for eks targets. It uses the default AWS credential chain; credentials_ref, when
// set, names a profile in the shared AWS config.
func New(ctx context.Context, t agentproto.Target) (collectors.Collector, error) {
	if t.Eks == nil || t.Eks.Region == "" {
		return nil, errors.New("eks target has no region")
	}
	profile := ""
	if t.CredentialsRef != nil {
		profile = *t.CredentialsRef
	}
	api, err := Client(ctx, t.Eks.Region, profile)
	if err != nil {
		return nil, err
	}
	return NewWithAPI(api, t.Eks.Region), nil
}

// Client is an EKS client for region, with the default credential chain or a profile of the shared AWS config.
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
	return eks.NewFromConfig(cfg), nil
}

// NewWithAPI returns a collector using api for the clusters of region.
func NewWithAPI(api API, region string) *Collector { return &Collector{api: api, region: region} }

// Collector reads the EKS clusters of a region. A cluster's control plane and each managed node group are
// workloads of their own, labelled with goliash.service as the cluster, so the cluster is one service whose
// versions show node groups left behind.
type Collector struct {
	api    API
	region string
}

// Collect lists the region's clusters and their node groups.
func (c *Collector) Collect(ctx context.Context) (collectors.Result, error) {
	res := collectors.Result{Complete: true, Workloads: []agentproto.Workload{}}
	var names []string
	var token *string
	for {
		out, err := c.api.ListClusters(ctx, &eks.ListClustersInput{NextToken: token})
		if err != nil {
			return res, fmt.Errorf("list clusters: %w", err)
		}
		names = append(names, out.Clusters...)
		if token = out.NextToken; token == nil {
			break
		}
	}
	for _, name := range names {
		out, err := c.api.DescribeCluster(ctx, &eks.DescribeClusterInput{Name: aws.String(name)})
		if err != nil {
			res.Complete, res.Errors = false, append(res.Errors, "describe cluster "+name+": "+err.Error())
			continue
		}
		cl := out.Cluster
		if cl == nil || cl.Status == types.ClusterStatusDeleting {
			continue
		}
		w := c.workload(aws.ToString(cl.Arn), agentproto.EksCluster, name, name, "control-plane", aws.ToString(cl.Version), 1, cl.Tags)
		if v := cl.ResourcesVpcConfig; v != nil && v.EndpointPublicAccess && slices.Contains(v.PublicAccessCidrs, "0.0.0.0/0") {
			w.Exposed = []string{"public API endpoint " + aws.ToString(cl.Endpoint)}
		}
		res.Workloads = append(res.Workloads, w)
		if err := c.nodegroups(ctx, cl, &res); err != nil {
			res.Complete, res.Errors = false, append(res.Errors, "node groups of "+name+": "+err.Error())
		}
	}
	sort.Slice(res.Workloads, func(i, j int) bool { return res.Workloads[i].ID < res.Workloads[j].ID })
	return res, nil
}

func (c *Collector) nodegroups(ctx context.Context, cl *types.Cluster, res *collectors.Result) error {
	var token *string
	for {
		out, err := c.api.ListNodegroups(ctx, &eks.ListNodegroupsInput{ClusterName: cl.Name, NextToken: token})
		if err != nil {
			return err
		}
		for _, ng := range out.Nodegroups {
			d, err := c.api.DescribeNodegroup(ctx, &eks.DescribeNodegroupInput{ClusterName: cl.Name, NodegroupName: aws.String(ng)})
			if err != nil {
				return err
			}
			g := d.Nodegroup
			if g == nil || g.Status == types.NodegroupStatusDeleting {
				continue
			}
			nodes := 0
			if g.ScalingConfig != nil {
				nodes = int(aws.ToInt32(g.ScalingConfig.DesiredSize))
			}
			tags := map[string]string{}
			for k, v := range cl.Tags { // the cluster's tags place its node groups with it
				tags[k] = v
			}
			for k, v := range g.Tags {
				tags[k] = v
			}
			w := c.workload(aws.ToString(g.NodegroupArn), agentproto.EksNodegroup, aws.ToString(cl.Name)+"/"+ng, aws.ToString(cl.Name),
				"nodes", aws.ToString(g.Version), nodes, tags)
			w.DesiredReplicas = &nodes
			if r := aws.ToString(g.ReleaseVersion); r != "" {
				w.Labels["goliash.eks/release"] = r // the node AMI release, e.g. 1.31.3-20250103
			}
			res.Workloads = append(res.Workloads, w)
		}
		if token = out.NextToken; token == nil {
			return nil
		}
	}
}

func (c *Collector) workload(id string, kind agentproto.WorkloadKind, name, cluster, container, version string, running int, tags map[string]string) agentproto.Workload {
	region := c.region
	labels := map[string]string{"goliash.service": cluster}
	for k, v := range tags {
		labels[k] = v
	}
	return agentproto.Workload{
		ID: id, Kind: kind, Namespace: &region, Name: name, Labels: labels,
		Containers: []agentproto.Container{{Name: container, Image: Image + ":" + version, Running: running}},
	}
}

// Versions lists the Kubernetes versions EKS offers, in standard or extended support, as tags of Image.
func Versions(ctx context.Context, api API, repo string) ([]string, error) {
	if repo != Image {
		return nil, fmt.Errorf("%s: not EKS", repo)
	}
	var out []string
	var token *string
	for {
		page, err := api.DescribeClusterVersions(ctx, &eks.DescribeClusterVersionsInput{NextToken: token})
		if err != nil {
			return nil, err
		}
		for _, v := range page.ClusterVersions {
			if s := strings.TrimSpace(aws.ToString(v.ClusterVersion)); s != "" && !slices.Contains(out, s) {
				out = append(out, s)
			}
		}
		if token = page.NextToken; token == nil {
			return out, nil
		}
	}
}
