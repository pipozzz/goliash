// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: Apache-2.0

// Package rds collects the Amazon RDS and Aurora databases of a region, using rds:DescribeDBInstances and
// rds:DescribeDBClusters only, and lists the engine versions RDS offers (rds:DescribeDBEngineVersions).
package rds

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/rds"
	"github.com/aws/aws-sdk-go-v2/service/rds/types"

	"github.com/pipozzz/goliash/internal/collectors"
	"github.com/pipozzz/goliash/pkg/agentproto"
)

// Host is the registry host of the images databases report: rds.amazonaws.com/postgres:16.4. It names no real
// registry; the agent answers for it with the engine versions RDS offers.
const Host = "rds.amazonaws.com"

// engines are those reported, the way RDS names them. Oracle and SQL Server versions, and DocumentDB and Neptune
// (which RDS lists too), are left out.
var engines = map[string]bool{"postgres": true, "mysql": true, "mariadb": true, "aurora-postgresql": true, "aurora-mysql": true}

// API is the part of the RDS client the collector uses.
type API interface {
	DescribeDBInstances(ctx context.Context, in *rds.DescribeDBInstancesInput, opts ...func(*rds.Options)) (*rds.DescribeDBInstancesOutput, error)
	DescribeDBClusters(ctx context.Context, in *rds.DescribeDBClustersInput, opts ...func(*rds.Options)) (*rds.DescribeDBClustersOutput, error)
	DescribeDBEngineVersions(ctx context.Context, in *rds.DescribeDBEngineVersionsInput, opts ...func(*rds.Options)) (*rds.DescribeDBEngineVersionsOutput, error)
}

// New is the collectors.Factory for rds targets. It uses the default AWS credential chain; credentials_ref, when
// set, names a profile in the shared AWS config.
func New(ctx context.Context, t agentproto.Target) (collectors.Collector, error) {
	if t.Rds == nil || t.Rds.Region == "" {
		return nil, errors.New("rds target has no region")
	}
	profile := ""
	if t.CredentialsRef != nil {
		profile = *t.CredentialsRef
	}
	api, err := Client(ctx, t.Rds.Region, profile)
	if err != nil {
		return nil, err
	}
	return &Collector{api: api, region: t.Rds.Region}, nil
}

// Client is an RDS client for region, with the default credential chain or a profile of the shared AWS config.
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
	return rds.NewFromConfig(cfg), nil
}

// NewWithAPI returns a collector using api for the databases of region.
func NewWithAPI(api API, region string) *Collector { return &Collector{api: api, region: region} }

// Collector reads the RDS databases of a region: each instance on its own, and each cluster (Aurora, or a
// Multi-AZ DB cluster) once with its instances.
type Collector struct {
	api    API
	region string
}

// Collect lists the region's databases.
func (c *Collector) Collect(ctx context.Context) (collectors.Result, error) {
	var instances []types.DBInstance
	var marker *string
	for {
		out, err := c.api.DescribeDBInstances(ctx, &rds.DescribeDBInstancesInput{Marker: marker})
		if err != nil {
			return collectors.Result{}, fmt.Errorf("describe db instances: %w", err)
		}
		instances = append(instances, out.DBInstances...)
		if marker = out.Marker; marker == nil {
			break
		}
	}
	res := collectors.Result{Complete: true, Workloads: []agentproto.Workload{}}
	public := map[string][]string{} // cluster -> its publicly accessible instances' endpoints
	for _, in := range instances {
		if cl := aws.ToString(in.DBClusterIdentifier); cl != "" {
			if aws.ToBool(in.PubliclyAccessible) {
				public[cl] = append(public[cl], endpoint(in))
			}
			continue
		}
		if w, ok := c.instance(in); ok {
			res.Workloads = append(res.Workloads, w)
		}
	}
	marker = nil
	for {
		out, err := c.api.DescribeDBClusters(ctx, &rds.DescribeDBClustersInput{Marker: marker})
		if err != nil {
			// Without rds:DescribeDBClusters the instances still count; the clusters' do not.
			res.Complete, res.Errors = false, append(res.Errors, "describe db clusters: "+err.Error())
			break
		}
		for _, cl := range out.DBClusters {
			if w, ok := c.cluster(cl, public[aws.ToString(cl.DBClusterIdentifier)]); ok {
				res.Workloads = append(res.Workloads, w)
			}
		}
		if marker = out.Marker; marker == nil {
			break
		}
	}
	sort.Slice(res.Workloads, func(i, j int) bool { return res.Workloads[i].ID < res.Workloads[j].ID })
	return res, nil
}

func (c *Collector) instance(in types.DBInstance) (agentproto.Workload, bool) {
	engine := aws.ToString(in.Engine)
	if !engines[engine] {
		return agentproto.Workload{}, false
	}
	running := 0
	if aws.ToString(in.DBInstanceStatus) != "stopped" {
		running = 1
	}
	w := c.workload(aws.ToString(in.DBInstanceArn), agentproto.RdsInstance, aws.ToString(in.DBInstanceIdentifier), engine,
		aws.ToString(in.EngineVersion), running, in.TagList)
	if aws.ToBool(in.PubliclyAccessible) {
		w.Exposed = []string{"public endpoint " + endpoint(in)}
	}
	return w, true
}

func (c *Collector) cluster(cl types.DBCluster, public []string) (agentproto.Workload, bool) {
	engine := aws.ToString(cl.Engine)
	if !engines[engine] {
		return agentproto.Workload{}, false
	}
	running := len(cl.DBClusterMembers)
	if aws.ToString(cl.Status) == "stopped" {
		running = 0
	}
	w := c.workload(aws.ToString(cl.DBClusterArn), agentproto.RdsCluster, aws.ToString(cl.DBClusterIdentifier), engine,
		aws.ToString(cl.EngineVersion), running, cl.TagList)
	for _, e := range public {
		w.Exposed = append(w.Exposed, "public endpoint "+e)
	}
	return w, true
}

func (c *Collector) workload(id string, kind agentproto.WorkloadKind, name, engine, version string, running int, tags []types.Tag) agentproto.Workload {
	region := c.region
	w := agentproto.Workload{
		ID: id, Kind: kind, Namespace: &region, Name: name,
		Containers: []agentproto.Container{{Name: engine, Image: Host + "/" + engine + ":" + Version(engine, version), Running: running}},
	}
	if len(tags) > 0 {
		w.Labels = map[string]string{}
		for _, t := range tags {
			w.Labels[aws.ToString(t.Key)] = aws.ToString(t.Value)
		}
	}
	return w
}

func endpoint(in types.DBInstance) string {
	if in.Endpoint != nil && in.Endpoint.Address != nil {
		return aws.ToString(in.Endpoint.Address)
	}
	return aws.ToString(in.DBInstanceIdentifier)
}

// Version is an engine version as Goliash compares it: Aurora MySQL's own version (8.0.mysql_aurora.3.05.2 is
// 3.05.2), which its release cycles follow; others as RDS names them.
func Version(engine, v string) string {
	if engine == "aurora-mysql" {
		if _, aurora, ok := strings.Cut(v, ".mysql_aurora."); ok {
			return aurora
		}
	}
	return v
}

// EngineVersions lists the versions RDS offers of an engine, the repository rds.amazonaws.com/<engine>, as tags.
func EngineVersions(ctx context.Context, api API, repo string) ([]string, error) {
	engine, ok := strings.CutPrefix(repo, Host+"/")
	if !ok || !engines[engine] {
		return nil, fmt.Errorf("%s: not an RDS engine", repo)
	}
	var out []string
	seen := map[string]bool{}
	var marker *string
	for {
		page, err := api.DescribeDBEngineVersions(ctx, &rds.DescribeDBEngineVersionsInput{Engine: &engine, Marker: marker})
		if err != nil {
			return nil, err
		}
		for _, v := range page.DBEngineVersions {
			if t := Version(engine, aws.ToString(v.EngineVersion)); t != "" && !seen[t] {
				seen[t] = true
				out = append(out, t)
			}
		}
		if marker = page.Marker; marker == nil {
			return out, nil
		}
	}
}
