// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: Apache-2.0

package msk

import (
	"context"
	"slices"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/kafka"
	"github.com/aws/aws-sdk-go-v2/service/kafka/types"

	"github.com/pipozzz/goliash/pkg/agentproto"
)

type fakeAPI struct {
	clusters []types.Cluster
	versions []types.KafkaVersion
}

func (f *fakeAPI) ListClustersV2(context.Context, *kafka.ListClustersV2Input, ...func(*kafka.Options)) (*kafka.ListClustersV2Output, error) {
	return &kafka.ListClustersV2Output{ClusterInfoList: f.clusters}, nil
}

func (f *fakeAPI) ListKafkaVersions(context.Context, *kafka.ListKafkaVersionsInput, ...func(*kafka.Options)) (*kafka.ListKafkaVersionsOutput, error) {
	return &kafka.ListKafkaVersionsOutput{KafkaVersions: f.versions}, nil
}

func TestCollect(t *testing.T) {
	api := &fakeAPI{clusters: []types.Cluster{
		{
			ClusterName: aws.String("events"), ClusterArn: aws.String("arn:aws:kafka:eu-west-1:1:cluster/events/x"), State: types.ClusterStateActive,
			Tags: map[string]string{"goliash.env": "prod"},
			Provisioned: &types.Provisioned{
				NumberOfBrokerNodes: aws.Int32(3), CurrentBrokerSoftwareInfo: &types.BrokerSoftwareInfo{KafkaVersion: aws.String("3.6.0")},
				BrokerNodeGroupInfo: &types.BrokerNodeGroupInfo{ConnectivityInfo: &types.ConnectivityInfo{
					PublicAccess: &types.PublicAccess{Type: aws.String("SERVICE_PROVIDED_EIPS")},
				}},
			},
		},
		{ClusterName: aws.String("serverless"), ClusterArn: aws.String("arn:aws:kafka:eu-west-1:1:cluster/serverless/y"), Serverless: &types.Serverless{}},
	}}
	res, err := NewWithAPI(api, "eu-west-1").Collect(context.Background())
	if err != nil || len(res.Workloads) != 1 {
		t.Fatalf("collect: %+v %v", res, err)
	}
	w := res.Workloads[0]
	if w.Kind != agentproto.MskCluster || w.Containers[0].Image != "kafka.amazonaws.com/kafka:3.6.0" || w.Containers[0].Running != 3 ||
		w.Labels["goliash.env"] != "prod" || len(w.Exposed) != 1 {
		t.Errorf("events %+v", w)
	}
}

func TestVersions(t *testing.T) {
	api := &fakeAPI{versions: []types.KafkaVersion{
		{Version: aws.String("3.6.0"), Status: types.KafkaVersionStatusDeprecated},
		{Version: aws.String("3.7.x"), Status: types.KafkaVersionStatusActive},
		{Version: aws.String("3.7.x.kraft"), Status: types.KafkaVersionStatusActive},
		{Version: aws.String("3.9.x"), Status: types.KafkaVersionStatusActive},
	}}
	if got, _ := Versions(context.Background(), api, Image); !slices.Equal(got, []string{"3.7", "3.9"}) {
		t.Errorf("versions %v", got)
	}
}
