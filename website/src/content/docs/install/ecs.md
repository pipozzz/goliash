---
title: Amazon ECS and Lambda
description: 'Watch Amazon ECS clusters, Lambda functions and private ECR repositories with the Goliash agent and a read-only IAM task role.'
---

## Try it in one line

**Connect → Amazon ECS → Get the command** in Goliash gives this with your server and a fresh enrollment code. It
deploys the agent with CloudFormation into the default VPC, as a Fargate service in a small cluster of its own:

```sh
curl -fsSLo goliash-agent.cfn.yaml https://raw.githubusercontent.com/pipozzz/goliash/main/deploy/ecs/goliash-agent.cfn.yaml && \
aws cloudformation deploy --stack-name goliash-agent --template-file goliash-agent.cfn.yaml --capabilities CAPABILITY_IAM \
  --parameter-overrides ServerURL=https://goliash.example.com EnrollCode=glsh_enroll_… \
  Subnets=$(aws ec2 describe-subnets --filters Name=default-for-az,Values=true --query 'Subnets[].SubnetId' --output text | tr '\t' ,)
```

Within a minute the agent registers and adds every ECS cluster of the region, each as a target, and the region's
Lambda functions (`WatchLambda=true`, the default). Its log: `aws logs tail /ecs/goliash-agent --follow`. Remove it
all with `aws cloudformation delete-stack --stack-name goliash-agent`.

## CloudFormation

`deploy/ecs/goliash-agent.cfn.yaml` creates the task and execution roles, a Secrets Manager secret holding the
code, a log group and the service. Parameters:

| Parameter | Default | Meaning |
| --- | --- | --- |
| `ServerURL`, `EnrollCode` | | Where to report, and the code from Connect (or an agent token) |
| `Subnets` | | Where the task runs; private subnets need a NAT gateway |
| `AssignPublicIp` | `ENABLED` | `DISABLED` in private subnets |
| `Cluster` | new cluster | Run in a cluster you have instead |
| `SecurityGroups` | the VPC's default | The agent only connects out |
| `Version` | `latest` | Agent image tag; the server's version fits best |
| `WatchLambda` | `true` | Also read Lambda functions and aliases |
| `Regions`, `Clusters` | | More regions; only these clusters |

## Terraform

The Terraform module in `deploy/ecs/goliash-agent` runs the agent as a Fargate service. Its task role may only call
`ecs:List*`, `ecs:Describe*` and `ecr:ListImages`.

```hcl
module "goliash_agent" {
  source             = "github.com/pipozzz/goliash//deploy/ecs/goliash-agent"
  cluster_arn        = aws_ecs_cluster.tools.arn
  server_url         = "https://goliash.example.com"
  token_secret_arn   = aws_secretsmanager_secret.goliash_agent_token.arn
  subnet_ids         = module.vpc.private_subnets
  security_group_ids = [aws_security_group.egress_only.id]
}
```

The agent needs outbound HTTPS only. With an [enrollment code](/install/#enrollment-codes) in the secret it adds every
ECS cluster of its region by itself, each as a target of its own, so the staging cluster can go to staging and the
production one to production. `regions = ["us-east-1"]` adds the clusters of more regions and
`clusters = ["prod", "staging"]` keeps only some. With an agent token, create an `ecs` target for it with
`{"ecs":{"region":"eu-west-1"}}`, and optionally `"clusters": [...]` to limit it to some clusters.

## Lambda functions

Set `watch_lambda = true` and the task role may also call `lambda:ListFunctions`, `lambda:GetFunction` and
`lambda:ListAliases`. An agent
that enrolls with a code then adds the region's functions as a `lambda` target, named `lambda-<region>`; any agent
with AWS credentials that may list functions does the same (`GOLIASH_LAMBDA=off` stops it). With a token, create the
target with `{"lambda":{"region":"eu-west-1"}}`, optionally with `"name_prefixes": ["shop-"]`.

Aliases named like environments (`dev`, `staging`, `prod`) put each function's versions in those environments.
Map other names with `GOLIASH_LAMBDA_ALIASES=live=prod,canary=-` on an enrolling agent, or `alias_environments` in
the target's settings.

Container functions show their image like any service. A .zip function shows its runtime as a version
(`python 3.12`), with *new release* when AWS offers a newer runtime and *end of life* before AWS deprecates it. See
[Collectors](/reference/collectors/#target-settings).

## What the ECS collector reports

For every cluster in the region: services, their running tasks and the containers of those tasks' task
definitions, with digests. A deployment in progress shows both task definitions' versions.

## Private ECR repositories

Images in private ECR repositories (`<account>.dkr.ecr.<region>.amazonaws.com/…`) are checked by the agent through
the ECR API with the task role. Repositories in other accounts need a repository policy that allows
`ecr:ListImages` for the agent's role.

## Server

The server can run anywhere the team reaches it. On ECS, use PostgreSQL (`GOLIASH_DATABASE_URL`) rather than
SQLite, and set `GOLIASH_SECRET_KEY` from Secrets Manager.
