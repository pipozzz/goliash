// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: Apache-2.0

package eks

import (
	"context"
	"slices"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	"github.com/aws/aws-sdk-go-v2/service/eks/types"

	"github.com/pipozzz/goliash/pkg/agentproto"
)

type fakeAPI struct {
	clusters   map[string]types.Cluster
	nodegroups map[string][]types.Nodegroup
	versions   []string
}

func (f *fakeAPI) ListClusters(context.Context, *eks.ListClustersInput, ...func(*eks.Options)) (*eks.ListClustersOutput, error) {
	out := &eks.ListClustersOutput{}
	for name := range f.clusters {
		out.Clusters = append(out.Clusters, name)
	}
	slices.Sort(out.Clusters)
	return out, nil
}

func (f *fakeAPI) DescribeCluster(_ context.Context, in *eks.DescribeClusterInput, _ ...func(*eks.Options)) (*eks.DescribeClusterOutput, error) {
	c := f.clusters[aws.ToString(in.Name)]
	return &eks.DescribeClusterOutput{Cluster: &c}, nil
}

func (f *fakeAPI) ListNodegroups(_ context.Context, in *eks.ListNodegroupsInput, _ ...func(*eks.Options)) (*eks.ListNodegroupsOutput, error) {
	out := &eks.ListNodegroupsOutput{}
	for _, g := range f.nodegroups[aws.ToString(in.ClusterName)] {
		out.Nodegroups = append(out.Nodegroups, aws.ToString(g.NodegroupName))
	}
	return out, nil
}

func (f *fakeAPI) DescribeNodegroup(_ context.Context, in *eks.DescribeNodegroupInput, _ ...func(*eks.Options)) (*eks.DescribeNodegroupOutput, error) {
	for _, g := range f.nodegroups[aws.ToString(in.ClusterName)] {
		if aws.ToString(g.NodegroupName) == aws.ToString(in.NodegroupName) {
			return &eks.DescribeNodegroupOutput{Nodegroup: &g}, nil
		}
	}
	return &eks.DescribeNodegroupOutput{}, nil
}

func (f *fakeAPI) DescribeClusterVersions(context.Context, *eks.DescribeClusterVersionsInput, ...func(*eks.Options)) (*eks.DescribeClusterVersionsOutput, error) {
	out := &eks.DescribeClusterVersionsOutput{}
	for _, v := range f.versions {
		out.ClusterVersions = append(out.ClusterVersions, types.ClusterVersionInformation{ClusterVersion: aws.String(v)})
	}
	return out, nil
}

func TestCollect(t *testing.T) {
	api := &fakeAPI{
		clusters: map[string]types.Cluster{
			"prod": {
				Name: aws.String("prod"), Arn: aws.String("arn:aws:eks:eu-west-1:1:cluster/prod"), Version: aws.String("1.33"),
				Endpoint: aws.String("https://ABC.gr7.eu-west-1.eks.amazonaws.com"), Status: types.ClusterStatusActive,
				Tags:               map[string]string{"goliash.env": "prod"},
				ResourcesVpcConfig: &types.VpcConfigResponse{EndpointPublicAccess: true, PublicAccessCidrs: []string{"0.0.0.0/0"}},
			},
			"tools": {
				Name: aws.String("tools"), Arn: aws.String("arn:aws:eks:eu-west-1:1:cluster/tools"), Version: aws.String("1.31"),
				Status:             types.ClusterStatusActive,
				ResourcesVpcConfig: &types.VpcConfigResponse{EndpointPublicAccess: true, PublicAccessCidrs: []string{"10.0.0.0/8"}},
			},
		},
		nodegroups: map[string][]types.Nodegroup{"prod": {{
			NodegroupName: aws.String("general"), NodegroupArn: aws.String("arn:aws:eks:eu-west-1:1:nodegroup/prod/general/x"),
			Version: aws.String("1.32"), ReleaseVersion: aws.String("1.32.3-20250103"), Status: types.NodegroupStatusActive,
			ScalingConfig: &types.NodegroupScalingConfig{DesiredSize: aws.Int32(3)},
		}}},
	}
	res, err := NewWithAPI(api, "eu-west-1").Collect(context.Background())
	if err != nil || !res.Complete || len(res.Workloads) != 3 {
		t.Fatalf("collect: %+v %v", res, err)
	}
	got := map[string]agentproto.Workload{}
	for _, w := range res.Workloads {
		got[w.Name] = w
	}
	prod, ng, tools := got["prod"], got["prod/general"], got["tools"]
	if prod.Kind != agentproto.EksCluster || prod.Containers[0].Image != "eks.amazonaws.com/kubernetes:1.33" ||
		prod.Labels["goliash.service"] != "prod" || prod.Labels["goliash.env"] != "prod" || len(prod.Exposed) != 1 {
		t.Errorf("prod %+v", prod)
	}
	// The node group is the same service, a version behind, in the cluster's environment.
	if ng.Kind != agentproto.EksNodegroup || ng.Containers[0].Image != "eks.amazonaws.com/kubernetes:1.32" || ng.Containers[0].Running != 3 ||
		ng.Labels["goliash.service"] != "prod" || ng.Labels["goliash.env"] != "prod" || ng.Labels["goliash.eks/release"] != "1.32.3-20250103" {
		t.Errorf("node group %+v", ng)
	}
	if len(tools.Exposed) != 0 {
		t.Errorf("an endpoint open to 10.0.0.0/8 only is not exposed: %v", tools.Exposed)
	}
}

func TestVersions(t *testing.T) {
	api := &fakeAPI{versions: []string{"1.33", "1.32", "1.33"}}
	if got, err := Versions(context.Background(), api, Image); err != nil || !slices.Equal(got, []string{"1.33", "1.32"}) {
		t.Errorf("versions %v %v", got, err)
	}
}
