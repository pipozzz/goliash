// Copyright 2026 The Goliash Authors
// SPDX-License-Identifier: AGPL-3.0-only

package ui

import (
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"slices"
	"strings"

	"github.com/pipozzz/goliash/internal/auth"
	"github.com/pipozzz/goliash/internal/store"
)

// awsServices are the AWS services an agent can watch, in the order the page offers them.
var awsServices = []struct{ Key, Label, Env string }{
	{"ecs", "ECS clusters (and private ECR images)", ""},
	{"lambda", "Lambda functions", "GOLIASH_LAMBDA"},
	{"rds", "RDS and Aurora databases", "GOLIASH_RDS"},
	{"elasticache", "ElastiCache caches", "GOLIASH_ELASTICACHE"},
	{"eks", "EKS cluster versions", "GOLIASH_EKS"},
	{"opensearch", "OpenSearch domains", "GOLIASH_OPENSEARCH"},
	{"msk", "MSK (Kafka) clusters", "GOLIASH_MSK"},
}

// awsActions are the read-only IAM actions of each service.
var awsActions = map[string][]string{
	"ecs":         {"ecs:List*", "ecs:Describe*", "ecr:ListImages", "ecr:GetAuthorizationToken", "ecr:BatchGetImage", "ecr:GetDownloadUrlForLayer"},
	"lambda":      {"lambda:ListFunctions", "lambda:GetFunction", "lambda:ListAliases", "lambda:ListFunctionUrlConfigs"},
	"rds":         {"rds:DescribeDBInstances", "rds:DescribeDBClusters", "rds:DescribeDBEngineVersions", "rds:DescribePendingMaintenanceActions"},
	"elasticache": {"elasticache:Describe*", "elasticache:ListTagsForResource"},
	"eks":         {"eks:ListClusters", "eks:DescribeCluster", "eks:ListNodegroups", "eks:DescribeNodegroup", "eks:DescribeClusterVersions"},
	"opensearch":  {"es:ListDomainNames", "es:DescribeDomains", "es:ListTags", "es:ListVersions"},
	"msk":         {"kafka:ListClustersV2", "kafka:ListKafkaVersions"},
}

// AddAWSView is the page that gives an agent AWS: what to watch, the IAM policy, and the lines for its configuration.
type AddAWSView struct {
	Base
	Agent       AgentView
	Region      string
	Services    []AWSServiceOption
	Credentials string // keys, role or profile
	Runs        string // how the agent runs: docker, kubernetes or other
	Policy      string
	CreateIAM   string // aws CLI to give the credentials the policy
	Config      string // the lines to add to the agent's configuration
	Restart     string
	Enrolled    bool
}

// AWSServiceOption is one service to tick.
type AWSServiceOption struct {
	Key, Label string
	On         bool
}

var awsRegion = regexp.MustCompile(`^[a-z]{2}(-gov|-iso[a-z]*)?-[a-z]+-\d$`)

func (s *Server) addAWS(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	ctx := r.Context()
	a, err := s.store.GetAgent(ctx, p.Scope, r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		return back(w, r, "/agents", "error", "Unknown agent.")
	} else if err != nil {
		return err
	}
	q := r.URL.Query()
	v := AddAWSView{
		Base: s.base(ctx, p, "agents", "Add AWS to "+a.Name), Agent: agentView(a), Region: strings.TrimSpace(q.Get("region")),
		Credentials: q.Get("credentials"), Enrolled: a.CodeID != "",
	}
	if v.Region == "" || !awsRegion.MatchString(v.Region) {
		v.Region = "eu-central-1"
	}
	if v.Credentials != "role" && v.Credentials != "profile" {
		v.Credentials = "keys"
	}
	picked := q["service"]
	var on []string
	for _, svc := range awsServices {
		ticked := len(picked) == 0 || slices.Contains(picked, svc.Key)
		v.Services = append(v.Services, AWSServiceOption{Key: svc.Key, Label: svc.Label, On: ticked})
		if ticked {
			on = append(on, svc.Key)
		}
	}
	targets, err := s.store.ListAgentTargets(ctx, p.Scope, a.ID)
	if err != nil {
		return err
	}
	v.Runs = "other"
	for _, t := range targets {
		switch t.Platform {
		case "docker", "compose":
			v.Runs = "docker"
		case "kubernetes":
			v.Runs = "kubernetes"
		}
	}
	v.Policy = awsPolicy(on)
	v.CreateIAM, v.Config, v.Restart = awsSetup(v, on)
	return render(w, r, AddAWSPage(v))
}

// awsPolicy is the read-only IAM policy for the services.
func awsPolicy(services []string) string {
	type statement struct {
		Sid      string   `json:"Sid"`
		Effect   string   `json:"Effect"`
		Action   []string `json:"Action"`
		Resource string   `json:"Resource"`
	}
	doc := struct {
		Version   string      `json:"Version"`
		Statement []statement `json:"Statement"`
	}{Version: "2012-10-17"}
	for _, svc := range services {
		doc.Statement = append(doc.Statement, statement{Sid: "Goliash" + strings.ToUpper(svc[:1]) + svc[1:], Effect: "Allow", Action: awsActions[svc], Resource: "*"})
	}
	b, _ := json.MarshalIndent(doc, "", "  ")
	return string(b)
}

// awsSetup writes the commands that give the agent's credentials the policy, the lines for its configuration, and
// how to restart it.
func awsSetup(v AddAWSView, on []string) (iam, config, restart string) {
	var env []string
	env = append(env, "AWS_REGION="+v.Region)
	for _, svc := range awsServices {
		if svc.Env != "" && !slices.Contains(on, svc.Key) {
			env = append(env, svc.Env+"=off")
		}
	}
	if !slices.Contains(on, "ecs") {
		env = append(env, "GOLIASH_ECS_CLUSTERS=-") // no cluster is named -
	}
	policy := "aws iam put-%s-policy --%s-name %s --policy-name goliash-read --policy-document file://goliash-policy.json"
	switch v.Credentials {
	case "role":
		iam = "# Save the policy above as goliash-policy.json, then give it to the instance's (or pod's) role:\n" +
			strings.ReplaceAll(strings.ReplaceAll(policy, "%s-policy", "role-policy"), "--%s-name %s", "--role-name <the role>") + "\n" +
			"# On EC2, containers reach the instance's role only with a hop limit of 2:\n" +
			"aws ec2 modify-instance-metadata-options --instance-id <i-…> --http-put-response-hop-limit 2 --http-endpoint enabled"
	case "profile":
		iam = "# Save the policy above as goliash-policy.json; give it to the user or role of the profile, e.g.:\n" +
			"aws iam put-user-policy --user-name <user> --policy-name goliash-read --policy-document file://goliash-policy.json"
		env = append(env, "AWS_PROFILE=goliash")
	default:
		iam = "# Save the policy above as goliash-policy.json, then:\n" +
			"aws iam create-user --user-name goliash-agent\n" +
			"aws iam put-user-policy --user-name goliash-agent --policy-name goliash-read --policy-document file://goliash-policy.json\n" +
			"aws iam create-access-key --user-name goliash-agent   # gives the AccessKeyId and SecretAccessKey below"
		env = append(env, "AWS_ACCESS_KEY_ID=<AccessKeyId>", "AWS_SECRET_ACCESS_KEY=<SecretAccessKey>")
	}
	switch v.Runs {
	case "docker":
		var b strings.Builder
		b.WriteString("# goliash-agent.yml, under services → agent → environment:\n")
		for _, e := range env {
			k, val, _ := strings.Cut(e, "=")
			b.WriteString("      " + k + ": \"" + val + "\"\n")
		}
		if v.Credentials == "profile" {
			b.WriteString("# and under services → agent → volumes:\n      - ~/.aws:/home/nonroot/.aws:ro\n")
		}
		config = b.String()
		restart = "docker compose -f goliash-agent.yml up -d"
	case "kubernetes":
		var b strings.Builder
		b.WriteString("# values for the goliash-agent chart (with an IAM role for its service account, leave the keys out):\nenv:\n")
		for _, e := range env {
			k, val, _ := strings.Cut(e, "=")
			b.WriteString("  " + k + ": \"" + val + "\"\n")
		}
		b.WriteString("serviceAccount:\n  annotations:\n    eks.amazonaws.com/role-arn: <arn of the role with the policy>   # EKS (IRSA)\n")
		config = b.String()
		restart = "helm upgrade goliash-agent oci://ghcr.io/pipozzz/charts/goliash-agent -n goliash --reuse-values -f aws-values.yaml"
	default:
		config = "# environment of the agent:\n" + strings.Join(env, "\n")
		restart = "# restart the agent"
	}
	return iam, config, restart
}
