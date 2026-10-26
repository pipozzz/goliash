// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: Apache-2.0

package rds

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/rds"
	"github.com/aws/aws-sdk-go-v2/service/rds/types"

	"github.com/pipozzz/goliash/pkg/agentproto"
)

type fakeAPI struct {
	instances  [][]types.DBInstance // pages
	clusters   []types.DBCluster
	noClusters bool
	versions   map[string][]string
}

func (f *fakeAPI) DescribeDBInstances(_ context.Context, in *rds.DescribeDBInstancesInput, _ ...func(*rds.Options)) (*rds.DescribeDBInstancesOutput, error) {
	i := 0
	if in.Marker != nil {
		i = 1
	}
	out := &rds.DescribeDBInstancesOutput{DBInstances: f.instances[i]}
	if i+1 < len(f.instances) {
		out.Marker = aws.String("next")
	}
	return out, nil
}

func (f *fakeAPI) DescribeDBClusters(context.Context, *rds.DescribeDBClustersInput, ...func(*rds.Options)) (*rds.DescribeDBClustersOutput, error) {
	if f.noClusters {
		return nil, errors.New("AccessDenied")
	}
	return &rds.DescribeDBClustersOutput{DBClusters: f.clusters}, nil
}

func (f *fakeAPI) DescribeDBEngineVersions(_ context.Context, in *rds.DescribeDBEngineVersionsInput, _ ...func(*rds.Options)) (*rds.DescribeDBEngineVersionsOutput, error) {
	out := &rds.DescribeDBEngineVersionsOutput{}
	for _, v := range f.versions[aws.ToString(in.Engine)] {
		out.DBEngineVersions = append(out.DBEngineVersions, types.DBEngineVersion{EngineVersion: aws.String(v)})
	}
	return out, nil
}

func instance(id, engine, version, cluster string, public bool) types.DBInstance {
	in := types.DBInstance{
		DBInstanceIdentifier: aws.String(id), DBInstanceArn: aws.String("arn:aws:rds:eu-west-1:1:db:" + id),
		Engine: aws.String(engine), EngineVersion: aws.String(version), DBInstanceStatus: aws.String("available"),
		PubliclyAccessible: aws.Bool(public), Endpoint: &types.Endpoint{Address: aws.String(id + ".abc.eu-west-1.rds.amazonaws.com")},
	}
	if cluster != "" {
		in.DBClusterIdentifier = aws.String(cluster)
	}
	return in
}

func TestCollect(t *testing.T) {
	api := &fakeAPI{
		instances: [][]types.DBInstance{
			{instance("orders-db", "postgres", "13.15", "", true), instance("legacy", "sqlserver-se", "15.00.4345.5.v1", "", false)},
			{instance("shop-1", "aurora-mysql", "8.0.mysql_aurora.3.05.2", "shop", false), instance("shop-2", "aurora-mysql", "8.0.mysql_aurora.3.05.2", "shop", true)},
		},
		clusters: []types.DBCluster{{
			DBClusterIdentifier: aws.String("shop"), DBClusterArn: aws.String("arn:aws:rds:eu-west-1:1:cluster:shop"),
			Engine: aws.String("aurora-mysql"), EngineVersion: aws.String("8.0.mysql_aurora.3.05.2"), Status: aws.String("available"),
			DBClusterMembers: []types.DBClusterMember{{}, {}},
			TagList:          []types.Tag{{Key: aws.String("goliash.env"), Value: aws.String("prod")}},
		}},
	}
	res, err := NewWithAPI(api, "eu-west-1").Collect(context.Background())
	if err != nil || !res.Complete || len(res.Workloads) != 2 {
		t.Fatalf("collect: %+v %v", res, err)
	}
	shop, orders := res.Workloads[0], res.Workloads[1] // by ARN: cluster before db
	if shop.Kind != agentproto.RdsCluster || shop.Name != "shop" || shop.Containers[0].Image != "rds.amazonaws.com/aurora-mysql:3.05.2" ||
		shop.Containers[0].Running != 2 || shop.Labels["goliash.env"] != "prod" ||
		!slices.Equal(shop.Exposed, []string{"public endpoint shop-2.abc.eu-west-1.rds.amazonaws.com"}) {
		t.Errorf("cluster %+v", shop)
	}
	if orders.Kind != agentproto.RdsInstance || orders.Containers[0].Image != "rds.amazonaws.com/postgres:13.15" ||
		*orders.Namespace != "eu-west-1" || len(orders.Exposed) != 1 {
		t.Errorf("instance %+v", orders)
	}

	// Without rds:DescribeDBClusters: the instances, incomplete.
	api.noClusters = true
	res, _ = NewWithAPI(api, "eu-west-1").Collect(context.Background())
	if res.Complete || len(res.Workloads) != 1 || len(res.Errors) != 1 {
		t.Errorf("no clusters: %+v", res)
	}
}

func TestEngineVersions(t *testing.T) {
	api := &fakeAPI{versions: map[string][]string{
		"postgres":     {"13.15", "16.4", "17.2"},
		"aurora-mysql": {"8.0.mysql_aurora.3.05.2", "8.0.mysql_aurora.3.08.0"},
	}}
	if got, err := EngineVersions(context.Background(), api, "rds.amazonaws.com/aurora-mysql"); err != nil || !slices.Equal(got, []string{"3.05.2", "3.08.0"}) {
		t.Errorf("aurora-mysql: %v %v", got, err)
	}
	if got, _ := EngineVersions(context.Background(), api, "rds.amazonaws.com/postgres"); len(got) != 3 {
		t.Errorf("postgres: %v", got)
	}
	if _, err := EngineVersions(context.Background(), api, "rds.amazonaws.com/oracle-ee"); err == nil {
		t.Error("an engine left out was listed")
	}
}
