// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: Apache-2.0

package ecr

import (
	"context"
	"encoding/base64"
	"errors"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecr"
	"github.com/aws/aws-sdk-go-v2/service/ecr/types"
)

func TestParse(t *testing.T) {
	for in, want := range map[string][3]string{
		"123456789012.dkr.ecr.eu-west-1.amazonaws.com/team/payments": {"123456789012", "eu-west-1", "team/payments"},
		"123456789012.dkr.ecr-fips.us-east-1.amazonaws.com/app":      {"123456789012", "us-east-1", "app"},
		"123456789012.dkr.ecr.cn-north-1.amazonaws.com.cn/app":       {"123456789012", "cn-north-1", "app"},
	} {
		a, r, n, ok := Parse(in)
		if !ok || [3]string{a, r, n} != want {
			t.Errorf("Parse(%q) = %s %s %s %v", in, a, r, n, ok)
		}
	}
	for _, in := range []string{"public.ecr.aws/nginx/nginx", "ghcr.io/acme/app", "12345.dkr.ecr.eu-west-1.amazonaws.com/app", "123456789012.dkr.ecr.eu-west-1.amazonaws.com.evil.com/app"} {
		if _, _, _, ok := Parse(in); ok {
			t.Errorf("Parse(%q) accepted", in)
		}
	}
}

type fakeECR struct {
	pages      [][]string
	calls      int
	err        error
	got        []*ecr.ListImagesInput
	tokenCalls int
}

func (f *fakeECR) GetAuthorizationToken(context.Context, *ecr.GetAuthorizationTokenInput, ...func(*ecr.Options)) (*ecr.GetAuthorizationTokenOutput, error) {
	f.tokenCalls++
	expires := time.Date(2026, 10, 9, 22, 0, 0, 0, time.UTC)
	return &ecr.GetAuthorizationTokenOutput{AuthorizationData: []types.AuthorizationData{{
		AuthorizationToken: aws.String(base64.StdEncoding.EncodeToString([]byte("AWS:tok3n"))), ExpiresAt: &expires,
	}}}, nil
}

func (f *fakeECR) ListImages(_ context.Context, in *ecr.ListImagesInput, _ ...func(*ecr.Options)) (*ecr.ListImagesOutput, error) {
	f.got = append(f.got, in)
	if f.err != nil {
		return nil, f.err
	}
	page := f.pages[f.calls]
	f.calls++
	out := &ecr.ListImagesOutput{}
	for _, t := range page {
		id := types.ImageIdentifier{ImageDigest: aws.String("sha256:x")}
		if t != "" {
			id.ImageTag = aws.String(t)
		}
		out.ImageIds = append(out.ImageIds, id)
	}
	if f.calls < len(f.pages) {
		out.NextToken = aws.String("next")
	}
	return out, nil
}

func TestListTags(t *testing.T) {
	fake := &fakeECR{pages: [][]string{{"1.0.0", "latest"}, {"1.1.0", "1.0.0", ""}}}
	var regions, profiles []string
	l := &Lister{NewAPI: func(_ context.Context, region, profile string) (API, error) {
		regions, profiles = append(regions, region), append(profiles, profile)
		return fake, nil
	}}
	repo := "123456789012.dkr.ecr.eu-west-1.amazonaws.com/team/payments"
	tags, err := l.ListTags(context.Background(), repo, "prod")
	if err != nil || len(tags) != 3 || tags[2] != "1.1.0" {
		t.Fatalf("tags %v %v", tags, err)
	}
	in := fake.got[0]
	if aws.ToString(in.RegistryId) != "123456789012" || aws.ToString(in.RepositoryName) != "team/payments" || in.Filter.TagStatus != types.TagStatusTagged {
		t.Fatalf("input %+v", in)
	}
	if aws.ToString(fake.got[1].NextToken) != "next" {
		t.Fatal("second page not requested with the token")
	}
	fake.calls = 0
	if _, err := l.ListTags(context.Background(), repo, "prod"); err != nil || len(regions) != 1 {
		t.Fatalf("client not reused: %v %v", regions, err)
	}
	if regions[0] != "eu-west-1" || profiles[0] != "prod" {
		t.Fatalf("client for %v %v", regions, profiles)
	}

	fake.err = errors.New("AccessDeniedException")
	if _, err := l.ListTags(context.Background(), repo, "prod"); err == nil {
		t.Fatal("error swallowed")
	}
	if _, err := l.ListTags(context.Background(), "ghcr.io/acme/app", ""); err == nil {
		t.Fatal("non-ECR accepted")
	}
}

func TestCredentials(t *testing.T) {
	fake := &fakeECR{}
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	l := &Lister{NewAPI: func(context.Context, string, string) (API, error) { return fake, nil }, now: func() time.Time { return now }}
	repo := "123456789012.dkr.ecr.eu-west-1.amazonaws.com/team/payments"
	c, err := l.Credentials(context.Background(), repo, "")
	if err != nil || c.Username != "AWS" || c.Password != "tok3n" {
		t.Fatalf("credentials %+v %v", c, err)
	}
	_, _ = l.Credentials(context.Background(), repo, "")
	if fake.tokenCalls != 1 {
		t.Fatalf("token fetched %d times while valid", fake.tokenCalls)
	}
	now = time.Date(2026, 10, 9, 21, 55, 0, 0, time.UTC) // within ten minutes of expiry
	_, _ = l.Credentials(context.Background(), repo, "")
	if fake.tokenCalls != 2 {
		t.Fatalf("token not renewed before expiry: %d", fake.tokenCalls)
	}
	if _, err := l.Credentials(context.Background(), "ghcr.io/acme/x", ""); err == nil {
		t.Error("credentials for a non-ECR repository")
	}
}
