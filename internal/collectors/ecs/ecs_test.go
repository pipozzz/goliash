// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: Apache-2.0

package ecs

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	"github.com/aws/aws-sdk-go-v2/service/ecs/types"

	"github.com/pipozzz/goliash/pkg/agentproto"
)

var (
	digOld = "sha256:" + strings.Repeat("a", 64)
	digNew = "sha256:" + strings.Repeat("b", 64)
)

// fakeECS serves a prod cluster with 12 services (to exercise batching) and a
// staging cluster the caller may not read.
type fakeECS struct {
	calls map[string]int
}

const prodARN = "arn:aws:ecs:eu-west-1:123456789012:cluster/prod"

func svcARN(name string) string { return "arn:aws:ecs:eu-west-1:123456789012:service/prod/" + name }

func (f *fakeECS) count(op string) { f.calls[op]++ }

func (f *fakeECS) ListClusters(_ context.Context, in *ecs.ListClustersInput, _ ...func(*ecs.Options)) (*ecs.ListClustersOutput, error) {
	f.count("ListClusters")
	if in.NextToken == nil {
		return &ecs.ListClustersOutput{ClusterArns: []string{prodARN}, NextToken: aws.String("p2")}, nil
	}
	return &ecs.ListClustersOutput{ClusterArns: []string{"arn:aws:ecs:eu-west-1:123456789012:cluster/staging"}}, nil
}

func (f *fakeECS) ListServices(_ context.Context, in *ecs.ListServicesInput, _ ...func(*ecs.Options)) (*ecs.ListServicesOutput, error) {
	f.count("ListServices")
	if strings.HasSuffix(*in.Cluster, "staging") {
		return nil, errors.New("AccessDeniedException: not authorized to perform ecs:ListServices")
	}
	var arns []string
	for i := range 12 {
		arns = append(arns, svcARN(fmt.Sprintf("svc-%02d", i)))
	}
	if in.NextToken == nil {
		return &ecs.ListServicesOutput{ServiceArns: arns[:7], NextToken: aws.String("s2")}, nil
	}
	return &ecs.ListServicesOutput{ServiceArns: arns[7:]}, nil
}

func (f *fakeECS) DescribeServices(_ context.Context, in *ecs.DescribeServicesInput, _ ...func(*ecs.Options)) (*ecs.DescribeServicesOutput, error) {
	f.count("DescribeServices")
	if len(in.Services) > 10 {
		return nil, errors.New("InvalidParameterException: too many services")
	}
	if len(in.Include) != 1 || in.Include[0] != types.ServiceFieldTags {
		return nil, errors.New("tags not requested")
	}
	out := &ecs.DescribeServicesOutput{}
	for _, arn := range in.Services {
		name := arn[strings.LastIndex(arn, "/")+1:]
		svc := types.Service{
			ServiceArn: aws.String(arn), ServiceName: aws.String(name), DesiredCount: 1,
			TaskDefinition: aws.String("td-" + name + ":1"),
			Deployments:    []types.Deployment{{Status: aws.String("PRIMARY"), TaskDefinition: aws.String("td-" + name + ":1")}},
		}
		if name == "svc-00" {
			svc.DesiredCount = 3
			svc.Tags = []types.Tag{{Key: aws.String("goliash.service"), Value: aws.String("payments-api")}}
			svc.Deployments = []types.Deployment{
				{Status: aws.String("PRIMARY"), TaskDefinition: aws.String("td-payments:8")},
				{Status: aws.String("ACTIVE"), TaskDefinition: aws.String("td-payments:7")},
			}
		}
		out.Services = append(out.Services, svc)
	}
	return out, nil
}

func (f *fakeECS) ListTasks(_ context.Context, in *ecs.ListTasksInput, _ ...func(*ecs.Options)) (*ecs.ListTasksOutput, error) {
	f.count("ListTasks")
	if in.DesiredStatus != types.DesiredStatusRunning {
		return nil, errors.New("desired status not RUNNING")
	}
	if *in.ServiceName == "svc-00" {
		return &ecs.ListTasksOutput{TaskArns: []string{"t1", "t2", "t3", "t4"}}, nil
	}
	return &ecs.ListTasksOutput{}, nil // other services scaled to zero
}

func (f *fakeECS) DescribeTasks(_ context.Context, in *ecs.DescribeTasksInput, _ ...func(*ecs.Options)) (*ecs.DescribeTasksOutput, error) {
	f.count("DescribeTasks")
	task := func(status, image, digest string) types.Task {
		return types.Task{LastStatus: aws.String(status), Containers: []types.Container{
			{Name: aws.String("app"), Image: aws.String(image), ImageDigest: aws.String(digest)},
			{Name: aws.String("datadog-agent"), Image: aws.String("public.ecr.aws/datadog/agent:7"), ImageDigest: aws.String("")},
		}}
	}
	return &ecs.DescribeTasksOutput{Tasks: []types.Task{
		task("RUNNING", "123456789012.dkr.ecr.eu-west-1.amazonaws.com/payments-api:1.4.2", digOld),
		task("RUNNING", "123456789012.dkr.ecr.eu-west-1.amazonaws.com/payments-api:1.4.2", digOld),
		task("RUNNING", "123456789012.dkr.ecr.eu-west-1.amazonaws.com/payments-api:1.5.0", digNew),
		task("PROVISIONING", "123456789012.dkr.ecr.eu-west-1.amazonaws.com/payments-api:1.5.0", digNew),
	}[:len(in.Tasks)]}, nil
}

func (f *fakeECS) DescribeTaskDefinition(_ context.Context, in *ecs.DescribeTaskDefinitionInput, _ ...func(*ecs.Options)) (*ecs.DescribeTaskDefinitionOutput, error) {
	f.count("DescribeTaskDefinition")
	name := strings.TrimPrefix(strings.Split(*in.TaskDefinition, ":")[0], "td-")
	return &ecs.DescribeTaskDefinitionOutput{TaskDefinition: &types.TaskDefinition{ContainerDefinitions: []types.ContainerDefinition{
		{Name: aws.String("app"), Image: aws.String("123456789012.dkr.ecr.eu-west-1.amazonaws.com/" + name + ":2.0.0")},
	}}}, nil
}

func TestCollectAllClusters(t *testing.T) {
	f := &fakeECS{calls: map[string]int{}}
	res, err := NewWithAPI(f, nil).Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.Complete || len(res.Errors) != 1 || !strings.Contains(res.Errors[0], "cluster staging") {
		t.Fatalf("complete=%v errors=%v", res.Complete, res.Errors)
	}
	if len(res.Workloads) != 12 {
		t.Fatalf("got %d workloads", len(res.Workloads))
	}
	if f.calls["ListClusters"] != 2 || f.calls["DescribeServices"] != 2 {
		t.Fatalf("pagination/batching calls %v", f.calls)
	}

	w := res.Workloads[0]
	if w.Name != "svc-00" || *w.Namespace != "prod" || w.Kind != agentproto.EcsService || *w.DesiredReplicas != 3 ||
		w.ID != svcARN("svc-00") || w.Labels["goliash.service"] != "payments-api" {
		t.Fatalf("workload %+v", w)
	}
	var got []string
	for _, c := range w.Containers {
		d := "-"
		if c.Digest != nil {
			d = (*c.Digest)[7:8]
		}
		got = append(got, fmt.Sprintf("%s=%s@%sx%d", c.Name, c.Image[strings.LastIndex(c.Image, "/")+1:], d, c.Running))
	}
	want := "app=payments-api:1.4.2@ax2 app=payments-api:1.5.0@bx1 datadog-agent=agent:7@-x3"
	if strings.Join(got, " ") != want {
		t.Fatalf("containers\n got: %s\nwant: %s", strings.Join(got, " "), want)
	}

	idle := res.Workloads[1]
	if len(idle.Containers) != 1 || idle.Containers[0].Running != 0 || !strings.HasSuffix(idle.Containers[0].Image, "svc-01:2.0.0") {
		t.Fatalf("scaled-to-zero service %+v", idle.Containers)
	}
}

func TestCollectNamedClusters(t *testing.T) {
	f := &fakeECS{calls: map[string]int{}}
	res, err := NewWithAPI(f, []string{"prod"}).Collect(context.Background())
	if err != nil || !res.Complete || len(res.Workloads) != 12 {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	if f.calls["ListClusters"] != 0 {
		t.Fatal("listed clusters although they were configured")
	}
}

func TestNewNeedsRegion(t *testing.T) {
	if _, err := New(context.Background(), agentproto.Target{}); err == nil {
		t.Fatal("missing region accepted")
	}
}
