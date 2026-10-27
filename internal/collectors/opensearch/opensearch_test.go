// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: Apache-2.0

package opensearch

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/opensearch"
	"github.com/aws/aws-sdk-go-v2/service/opensearch/types"

	"github.com/pipozzz/goliash/pkg/agentproto"
)

type fakeAPI struct {
	domains  []types.DomainStatus
	tags     map[string][]types.Tag
	versions []string
	batches  int
}

func (f *fakeAPI) ListDomainNames(context.Context, *opensearch.ListDomainNamesInput, ...func(*opensearch.Options)) (*opensearch.ListDomainNamesOutput, error) {
	out := &opensearch.ListDomainNamesOutput{}
	for _, d := range f.domains {
		out.DomainNames = append(out.DomainNames, types.DomainInfo{DomainName: d.DomainName})
	}
	return out, nil
}

func (f *fakeAPI) DescribeDomains(_ context.Context, in *opensearch.DescribeDomainsInput, _ ...func(*opensearch.Options)) (*opensearch.DescribeDomainsOutput, error) {
	f.batches++
	if len(in.DomainNames) > 5 {
		return nil, errors.New("ValidationException: at most 5 domains")
	}
	out := &opensearch.DescribeDomainsOutput{}
	for _, d := range f.domains {
		if slices.Contains(in.DomainNames, aws.ToString(d.DomainName)) {
			out.DomainStatusList = append(out.DomainStatusList, d)
		}
	}
	return out, nil
}

func (f *fakeAPI) ListTags(_ context.Context, in *opensearch.ListTagsInput, _ ...func(*opensearch.Options)) (*opensearch.ListTagsOutput, error) {
	return &opensearch.ListTagsOutput{TagList: f.tags[aws.ToString(in.ARN)]}, nil
}

func (f *fakeAPI) ListVersions(context.Context, *opensearch.ListVersionsInput, ...func(*opensearch.Options)) (*opensearch.ListVersionsOutput, error) {
	return &opensearch.ListVersionsOutput{Versions: f.versions}, nil
}

func domain(name, version string, vpc bool) types.DomainStatus {
	d := types.DomainStatus{
		DomainName: aws.String(name), ARN: aws.String("arn:aws:es:eu-west-1:1:domain/" + name), EngineVersion: aws.String(version),
		ClusterConfig: &types.ClusterConfig{InstanceCount: aws.Int32(3)},
	}
	if vpc {
		d.VPCOptions = &types.VPCDerivedInfo{VPCId: aws.String("vpc-1")}
		d.Endpoints = map[string]string{"vpc": "vpc-" + name + ".eu-west-1.es.amazonaws.com"}
	} else {
		d.Endpoint = aws.String("search-" + name + ".eu-west-1.es.amazonaws.com")
	}
	return d
}

func TestCollect(t *testing.T) {
	logs := domain("logs", "OpenSearch_2.11", false)
	logs.ServiceSoftwareOptions = &types.ServiceSoftwareOptions{UpdateAvailable: aws.Bool(true), NewVersion: aws.String("OpenSearch_2_11_R20250101-P1")}
	api := &fakeAPI{
		domains: []types.DomainStatus{logs, domain("legacy", "Elasticsearch_7.10", true)},
		tags:    map[string][]types.Tag{"arn:aws:es:eu-west-1:1:domain/logs": {{Key: aws.String("goliash.env"), Value: aws.String("prod")}}},
	}
	for i := range 5 { // more than one DescribeDomains batch
		api.domains = append(api.domains, domain("d"+string(rune('a'+i)), "OpenSearch_2.13", true))
	}
	res, err := NewWithAPI(api, "eu-west-1").Collect(context.Background())
	if err != nil || !res.Complete || len(res.Workloads) != 7 || api.batches != 2 {
		t.Fatalf("collect: %d workloads, %d batches, %v", len(res.Workloads), api.batches, err)
	}
	got := map[string]agentproto.Workload{}
	for _, w := range res.Workloads {
		got[w.Name] = w
	}
	l := got["logs"]
	if l.Kind != agentproto.OpensearchDomain || l.Containers[0].Image != "opensearch.amazonaws.com/opensearch:2.11" || l.Containers[0].Running != 3 ||
		l.Labels["goliash.env"] != "prod" || !slices.Equal(l.Maintenance, []string{"service software update OpenSearch_2_11_R20250101-P1"}) || len(l.Exposed) != 1 {
		t.Errorf("logs %+v", l)
	}
	if g := got["legacy"]; g.Containers[0].Image != "opensearch.amazonaws.com/elasticsearch:7.10" || len(g.Exposed) != 0 {
		t.Errorf("legacy %+v", g)
	}
}

func TestVersions(t *testing.T) {
	api := &fakeAPI{versions: []string{"OpenSearch_2.13", "OpenSearch_2.11", "Elasticsearch_7.10", "Elasticsearch_6.8"}}
	if got, _ := Versions(context.Background(), api, "opensearch.amazonaws.com/opensearch"); !slices.Equal(got, []string{"2.13", "2.11"}) {
		t.Errorf("opensearch %v", got)
	}
	if got, _ := Versions(context.Background(), api, "opensearch.amazonaws.com/elasticsearch"); !slices.Equal(got, []string{"7.10", "6.8"}) {
		t.Errorf("elasticsearch %v", got)
	}
}
