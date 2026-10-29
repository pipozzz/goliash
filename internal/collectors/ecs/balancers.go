// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: Apache-2.0

package ecs

import (
	"context"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	elbtypes "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2/types"
)

// A service registered with a load balancer is reachable from outside its VPC only when that load balancer is
// internet-facing: an internal one is reached from inside. The agent asks Elastic Load Balancing which load
// balancers a service's target groups belong to and their scheme (elasticloadbalancing:DescribeTargetGroups and
// DescribeLoadBalancers). Without those permissions it cannot tell, and counts the target group as before.

// ELBAPI is the subset of the Elastic Load Balancing v2 client the collector uses.
type ELBAPI interface {
	DescribeTargetGroups(ctx context.Context, in *elasticloadbalancingv2.DescribeTargetGroupsInput, opts ...func(*elasticloadbalancingv2.Options)) (*elasticloadbalancingv2.DescribeTargetGroupsOutput, error)
	DescribeLoadBalancers(ctx context.Context, in *elasticloadbalancingv2.DescribeLoadBalancersInput, opts ...func(*elasticloadbalancingv2.Options)) (*elasticloadbalancingv2.DescribeLoadBalancersOutput, error)
}

// balancerTTL is how long what Elastic Load Balancing said is kept: load balancers rarely change scheme.
const balancerTTL = 15 * time.Minute

// balancer is a load balancer as exposure needs it.
type balancer struct {
	Name, DNS string
	Public    bool // internet-facing
}

// balancers remembers target groups' load balancers.
type balancers struct {
	api ELBAPI

	mu     sync.Mutex
	groups map[string][]string // target group ARN -> load balancer ARNs
	lbs    map[string]balancer // load balancer ARN -> it
	at     time.Time
	denied bool // no permission: the scheme is unknown
}

// of returns the load balancers of target groups; ok is false when Elastic Load Balancing cannot tell.
func (b *balancers) of(ctx context.Context, groups []string) (map[string][]balancer, bool) {
	if b == nil || b.api == nil {
		return nil, false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if time.Since(b.at) > balancerTTL {
		b.groups, b.lbs, b.at, b.denied = map[string][]string{}, map[string]balancer{}, time.Now(), false
	}
	if b.denied {
		return nil, false
	}
	var ask []string
	for _, g := range groups {
		if _, ok := b.groups[g]; !ok {
			ask = append(ask, g)
		}
	}
	for start := 0; start < len(ask); start += 20 {
		out, err := b.api.DescribeTargetGroups(ctx, &elasticloadbalancingv2.DescribeTargetGroupsInput{TargetGroupArns: ask[start:min(start+20, len(ask))]})
		if err != nil {
			b.denied = true
			return nil, false
		}
		for _, tg := range out.TargetGroups {
			b.groups[aws.ToString(tg.TargetGroupArn)] = tg.LoadBalancerArns
		}
	}
	var lbAsk []string
	for _, g := range groups {
		for _, l := range b.groups[g] {
			if _, ok := b.lbs[l]; !ok {
				lbAsk = append(lbAsk, l)
				b.lbs[l] = balancer{} // asked once
			}
		}
	}
	for start := 0; start < len(lbAsk); start += 20 {
		out, err := b.api.DescribeLoadBalancers(ctx, &elasticloadbalancingv2.DescribeLoadBalancersInput{LoadBalancerArns: lbAsk[start:min(start+20, len(lbAsk))]})
		if err != nil {
			b.denied = true
			return nil, false
		}
		for _, l := range out.LoadBalancers {
			b.lbs[aws.ToString(l.LoadBalancerArn)] = balancer{
				Name: aws.ToString(l.LoadBalancerName), DNS: aws.ToString(l.DNSName), Public: l.Scheme == elbtypes.LoadBalancerSchemeEnumInternetFacing,
			}
		}
	}
	out := map[string][]balancer{}
	for _, g := range groups {
		for _, l := range b.groups[g] {
			out[g] = append(out[g], b.lbs[l])
		}
	}
	return out, true
}
