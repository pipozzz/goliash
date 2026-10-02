// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: Apache-2.0

package ecs

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	"github.com/aws/aws-sdk-go-v2/service/ecs/types"

	"github.com/pipozzz/goliash/internal/collectors"
	"github.com/pipozzz/goliash/pkg/agentproto"
)

// API is the subset of the ECS client the collector uses: List* and Describe* only,
// matching an IAM policy of ecs:List* and ecs:Describe*.
type API interface {
	ListClusters(ctx context.Context, in *ecs.ListClustersInput, opts ...func(*ecs.Options)) (*ecs.ListClustersOutput, error)
	ListServices(ctx context.Context, in *ecs.ListServicesInput, opts ...func(*ecs.Options)) (*ecs.ListServicesOutput, error)
	DescribeServices(ctx context.Context, in *ecs.DescribeServicesInput, opts ...func(*ecs.Options)) (*ecs.DescribeServicesOutput, error)
	ListTasks(ctx context.Context, in *ecs.ListTasksInput, opts ...func(*ecs.Options)) (*ecs.ListTasksOutput, error)
	DescribeTasks(ctx context.Context, in *ecs.DescribeTasksInput, opts ...func(*ecs.Options)) (*ecs.DescribeTasksOutput, error)
	DescribeTaskDefinition(ctx context.Context, in *ecs.DescribeTaskDefinitionInput, opts ...func(*ecs.Options)) (*ecs.DescribeTaskDefinitionOutput, error)
}

// New is the collectors.Factory for ECS targets. It uses the default AWS credential
// chain (environment, IRSA / ECS task role, instance profile); credentials_ref, when
// set, names a profile in the shared AWS config.
func New(ctx context.Context, t agentproto.Target) (collectors.Collector, error) {
	if t.Ecs == nil || t.Ecs.Region == "" {
		return nil, errors.New("ecs target has no region")
	}
	opts := []func(*awsconfig.LoadOptions) error{awsconfig.WithRegion(t.Ecs.Region)}
	if t.CredentialsRef != nil && *t.CredentialsRef != "" {
		opts = append(opts, awsconfig.WithSharedConfigProfile(*t.CredentialsRef))
	}
	cfg, err := awsconfig.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("aws config: %w", err)
	}
	return NewWithAPI(ecs.NewFromConfig(cfg), t.Ecs.Clusters), nil
}

// NewWithAPI returns a collector using api. clusters are names or ARNs; empty means all.
func NewWithAPI(api API, clusters []string) *Collector {
	return &Collector{api: api, clusters: clusters}
}

// Collector reads ECS services and the images their running tasks use.
type Collector struct {
	api      API
	clusters []string
}

// Collect walks clusters → services → running tasks. A cluster that cannot be read
// marks the result incomplete; the others are still reported.
func (c *Collector) Collect(ctx context.Context) (collectors.Result, error) {
	clusters := c.clusters
	if len(clusters) == 0 {
		var err error
		if clusters, err = c.listClusters(ctx); err != nil {
			return collectors.Result{}, err
		}
	}
	res := collectors.Result{Complete: true, Workloads: []agentproto.Workload{}}
	taskDefs := map[string][]types.ContainerDefinition{}
	for _, cluster := range clusters {
		ws, err := c.cluster(ctx, cluster, taskDefs)
		if err != nil {
			res.Complete = false
			res.Errors = append(res.Errors, fmt.Sprintf("cluster %s: %v", shortName(cluster), err))
		}
		res.Workloads = append(res.Workloads, ws...)
	}
	sort.SliceStable(res.Workloads, func(i, j int) bool {
		a, b := res.Workloads[i], res.Workloads[j]
		if *a.Namespace != *b.Namespace {
			return *a.Namespace < *b.Namespace
		}
		return a.Name < b.Name
	})
	return res, nil
}

func (c *Collector) listClusters(ctx context.Context) ([]string, error) {
	var arns []string
	var next *string
	for {
		out, err := c.api.ListClusters(ctx, &ecs.ListClustersInput{NextToken: next})
		if err != nil {
			return nil, fmt.Errorf("list clusters: %w", err)
		}
		arns = append(arns, out.ClusterArns...)
		if next = out.NextToken; next == nil {
			return arns, nil
		}
	}
}

func (c *Collector) cluster(ctx context.Context, cluster string, taskDefs map[string][]types.ContainerDefinition) ([]agentproto.Workload, error) {
	var serviceArns []string
	var next *string
	for {
		out, err := c.api.ListServices(ctx, &ecs.ListServicesInput{Cluster: &cluster, NextToken: next})
		if err != nil {
			return nil, fmt.Errorf("list services: %w", err)
		}
		serviceArns = append(serviceArns, out.ServiceArns...)
		if next = out.NextToken; next == nil {
			break
		}
	}

	var workloads []agentproto.Workload
	for batch := range chunks(serviceArns, 10) { // DescribeServices takes at most 10
		out, err := c.api.DescribeServices(ctx, &ecs.DescribeServicesInput{
			Cluster: &cluster, Services: batch, Include: []types.ServiceField{types.ServiceFieldTags},
		})
		if err != nil {
			return workloads, fmt.Errorf("describe services: %w", err)
		}
		for _, svc := range out.Services {
			w, err := c.service(ctx, cluster, svc, taskDefs)
			if err != nil {
				return workloads, fmt.Errorf("service %s: %w", aws.ToString(svc.ServiceName), err)
			}
			workloads = append(workloads, w)
		}
	}
	return workloads, nil
}

func (c *Collector) service(ctx context.Context, cluster string, svc types.Service, taskDefs map[string][]types.ContainerDefinition) (agentproto.Workload, error) {
	ns := shortName(cluster)
	desired := int(svc.DesiredCount)
	w := agentproto.Workload{
		ID: aws.ToString(svc.ServiceArn), Kind: agentproto.EcsService, Namespace: &ns, Name: aws.ToString(svc.ServiceName),
		DesiredReplicas: &desired, Containers: []agentproto.Container{},
	}
	if len(svc.Tags) > 0 {
		w.Labels = map[string]string{}
		for _, tag := range svc.Tags {
			w.Labels[aws.ToString(tag.Key)] = aws.ToString(tag.Value)
		}
	}

	var taskArns []string
	var next *string
	for {
		out, err := c.api.ListTasks(ctx, &ecs.ListTasksInput{
			Cluster: &cluster, ServiceName: svc.ServiceName, DesiredStatus: types.DesiredStatusRunning, NextToken: next,
		})
		if err != nil {
			return w, fmt.Errorf("list tasks: %w", err)
		}
		taskArns = append(taskArns, out.TaskArns...)
		if next = out.NextToken; next == nil {
			break
		}
	}

	type key struct{ name, image, digest string }
	counts := map[key]int{}
	for batch := range chunks(taskArns, 100) { // DescribeTasks takes at most 100
		out, err := c.api.DescribeTasks(ctx, &ecs.DescribeTasksInput{Cluster: &cluster, Tasks: batch})
		if err != nil {
			return w, fmt.Errorf("describe tasks: %w", err)
		}
		for _, task := range out.Tasks {
			if aws.ToString(task.LastStatus) != "RUNNING" {
				continue
			}
			for _, ct := range task.Containers {
				counts[key{aws.ToString(ct.Name), aws.ToString(ct.Image), aws.ToString(ct.ImageDigest)}]++
			}
		}
	}
	keys := make([]key, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].name != keys[j].name {
			return keys[i].name < keys[j].name
		}
		if keys[i].image != keys[j].image {
			return keys[i].image < keys[j].image
		}
		return keys[i].digest < keys[j].digest
	})
	seen := map[string]bool{}
	for _, k := range keys {
		ct := agentproto.Container{Name: k.name, Image: k.image, Running: counts[k]}
		if strings.HasPrefix(k.digest, "sha256:") {
			d := k.digest
			ct.Digest = &d
		}
		w.Containers = append(w.Containers, ct)
		seen[k.name] = true
	}

	// Containers with nothing running report the image of the primary deployment.
	if primary := primaryTaskDefinition(svc); primary != "" {
		defs, err := c.taskDefinition(ctx, primary, taskDefs)
		if err != nil {
			return w, err
		}
		for _, d := range defs {
			if name := aws.ToString(d.Name); !seen[name] {
				w.Containers = append(w.Containers, agentproto.Container{Name: name, Image: aws.ToString(d.Image), Running: 0})
			}
		}
	}
	return w, nil
}

func (c *Collector) taskDefinition(ctx context.Context, arn string, cache map[string][]types.ContainerDefinition) ([]types.ContainerDefinition, error) {
	if defs, ok := cache[arn]; ok {
		return defs, nil
	}
	out, err := c.api.DescribeTaskDefinition(ctx, &ecs.DescribeTaskDefinitionInput{TaskDefinition: &arn})
	if err != nil {
		return nil, fmt.Errorf("describe task definition: %w", err)
	}
	defs := out.TaskDefinition.ContainerDefinitions
	cache[arn] = defs
	return defs, nil
}

func primaryTaskDefinition(svc types.Service) string {
	for _, d := range svc.Deployments {
		if aws.ToString(d.Status) == "PRIMARY" {
			return aws.ToString(d.TaskDefinition)
		}
	}
	return aws.ToString(svc.TaskDefinition)
}

// shortName turns "arn:aws:ecs:eu-west-1:123:cluster/prod" into "prod".
func shortName(arnOrName string) string {
	if i := strings.LastIndex(arnOrName, "/"); i >= 0 {
		return arnOrName[i+1:]
	}
	return arnOrName
}

func chunks(items []string, size int) func(func([]string) bool) {
	return func(yield func([]string) bool) {
		for len(items) > 0 {
			n := min(size, len(items))
			if !yield(items[:n]) {
				return
			}
			items = items[n:]
		}
	}
}
