# ECS

Terraform module [`goliash-agent`](goliash-agent): the agent as a Fargate service whose task role may only call
`ecs:List*`, `ecs:Describe*`, `ecr:ListImages` (tags of private ECR repositories) and `ecr:GetAuthorizationToken`,
`ecr:BatchGetImage`, `ecr:GetDownloadUrlForLayer` (signatures, attestations and SBOMs of running images).

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

Then create an `ecs` target in Goliash with `{"ecs":{"region":"eu-west-1"}}` (optionally `"clusters":[...]`).
The server itself can run anywhere; on ECS use PostgreSQL (`GOLIASH_DATABASE_URL`) rather than SQLite.
