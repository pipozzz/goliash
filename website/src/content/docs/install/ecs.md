---
title: Amazon ECS
description: 'Watch Amazon ECS clusters and private ECR repositories with the Goliash agent and a read-only IAM task role.'
---

## Agent

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

The agent needs outbound HTTPS only. Create an `ecs` target for it with `{"ecs":{"region":"eu-west-1"}}`, and
optionally `"clusters": [...]` to limit it to some clusters.

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
