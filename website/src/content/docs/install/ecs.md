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

## Add AWS to an agent you already run

An agent on a Docker host or in a Kubernetes cluster can watch AWS too: **Agents → the agent → Add AWS to this
agent**. Pick the region, the services and how the agent gets credentials (a read-only IAM user's keys, the role
of the EC2 instance or pod, or a profile). The page writes, for that agent:

1. the read-only IAM policy for exactly those services, and the `aws` commands that give it to a new IAM user or
   to the instance's role (with the hop limit EC2 needs for containers);
2. the lines for its `goliash-agent.yml` (or Helm values);
3. the command that restarts it.

After the restart the agent finds what the credentials may read:

- **With an enrollment code**, it adds those targets by itself.
- **With a token**, it shows them on its page under **Found here, not watched**: tick them, pick an environment,
  and **Add**. The Agents list says when an agent found something (*3 found*).
- **Not collected** says what it could not read and why. A missing permission is named with the IAM actions to
  allow.

The agent keeps its identity (the Docker host or cluster) when it also gets AWS credentials.

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
`ecs:List*`, `ecs:Describe*`, `ecr:ListImages`, and for signatures and SBOMs of ECR images
`ecr:GetAuthorizationToken`, `ecr:BatchGetImage` and `ecr:GetDownloadUrlForLayer`.

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

## RDS and Aurora databases

With `WatchRDS=true` (CloudFormation, the default) or `watch_rds = true` (Terraform) the task role may also call
`rds:DescribeDBInstances`, `rds:DescribeDBClusters`, `rds:DescribeDBEngineVersions` and
`rds:DescribePendingMaintenanceActions`, and the agent adds the
region's databases as an `rds` target named `rds-<region>` (`GOLIASH_RDS=off` stops it). With a token, create the
target with `{"rds":{"region":"eu-west-1"}}`.

Each database is a service named like the instance, or the Aurora cluster with its instances, so it gets what any
service gets in the matrix, Updates, notifications and reports:

- **Its engine and version**, shown as `rds.amazonaws.com/postgres:16.4`. PostgreSQL, MySQL, MariaDB, Aurora
  PostgreSQL and Aurora MySQL are read. Aurora MySQL shows its own version (3.05.2 rather than
  8.0.mysql_aurora.3.05.2), which its support follows.
- **The versions RDS offers** in the region, as its upstream releases. A newer minor or major version shows as
  *upstream* drift and on Updates.
- **End of standard support** (from endoflife.date), as *end of life*. After that date AWS moves the database to
  RDS Extended Support and charges for it per vCPU and hour, unless the upgrade happens first.
- **Public accessibility.** A database with `PubliclyAccessible` is *exposed*, with its endpoint, and its upgrades
  come first.

Tag a database (or cluster) `goliash.ignore=true` to leave it out, and `goliash.env=prod` to place it in an
environment other than the target's.

**Patches waiting.** The maintenance RDS has pending for a database (OS patches, minor upgrades, certificate
rotation), ElastiCache's service updates (security updates first) and OpenSearch's service software updates show
as **patch pending** on the service page and on Updates, with what waits and when AWS applies it. The CSV and
`GET /api/v1/updates` have them as `maintenance`.

## ElastiCache caches

With `WatchElastiCache=true` (CloudFormation, the default) or `watch_elasticache = true` (Terraform) the task role
may also call `elasticache:Describe*` and `elasticache:ListTagsForResource`, and the agent adds the region's caches
as an `elasticache` target named `elasticache-<region>` (`GOLIASH_ELASTICACHE=off` stops it). With a token:
`{"elasticache":{"region":"eu-west-1"}}`.

Each replication group, standalone cluster (Memcached) and serverless cache is a service, its engine shown as
`elasticache.amazonaws.com/redis:7.1.0` (or `valkey`, `memcached`):

- **The versions ElastiCache offers** are its upstream releases, so newer engine versions show on Updates.
- **End of life** comes from endoflife.date: ElastiCache's own dates for Redis OSS (after which AWS charges for
  Extended Support), upstream Valkey and Memcached for those.
- **During an engine upgrade** the group shows both versions, like a rollout.
- **Tags:** `goliash.ignore=true` and `goliash.env` on the cache work as on databases. Without
  `elasticache:ListTagsForResource` caches still count, without their tags.

## EKS clusters

With `WatchEKS=true` (CloudFormation, the default) or `watch_eks = true` (Terraform) the task role may also call
`eks:ListClusters`, `eks:DescribeCluster`, `eks:ListNodegroups`, `eks:DescribeNodegroup` and
`eks:DescribeClusterVersions`, and the agent adds the region's clusters as an `eks` target named `eks-<region>`
(`GOLIASH_EKS=off` stops it). With a token: `{"eks":{"region":"eu-west-1"}}`.

This watches the clusters themselves; the workloads inside a cluster come from an agent in it (see
[Kubernetes](/install/kubernetes/)). Each cluster is a service named like it:

- **Its control plane and each managed node group** report their Kubernetes version as
  `eks.amazonaws.com/kubernetes:1.33`. A node group left a version behind shows as a second version of the
  cluster, with its node AMI release in the workload's labels.
- **The versions EKS offers** are its upstream releases, so the next Kubernetes version shows on Updates.
- **End of standard support** comes from endoflife.date (`amazon-eks`), as *end of life*. After it, AWS keeps the
  cluster in extended support and charges for it per cluster and hour, unless the cluster's upgrade policy says
  otherwise.
- **A public API endpoint** open to `0.0.0.0/0` makes the cluster *exposed*.
- **Tags:** `goliash.ignore=true` and `goliash.env` on the cluster work as on databases; node groups take the
  cluster's tags, then their own.

Self-managed nodes and Fargate profiles are not read.

## OpenSearch Service domains

With `WatchOpenSearch=true` (CloudFormation, the default) or `watch_opensearch = true` (Terraform) the task role may
also call `es:ListDomainNames`, `es:DescribeDomains`, `es:ListTags` and `es:ListVersions`, and the agent adds the
region's domains as an `opensearch` target named `opensearch-<region>` (`GOLIASH_OPENSEARCH=off` stops it).

Each domain is a service, its engine shown as `opensearch.amazonaws.com/opensearch:2.13`, or
`opensearch.amazonaws.com/elasticsearch:7.10` for legacy Elasticsearch:

- **The versions the service offers** are its upstream releases.
- **End of standard support** comes from endoflife.date (`amazon-opensearch`, `amazon-elasticsearch`), after which
  AWS charges for Extended Support.
- **A domain outside a VPC** (public endpoint) is *exposed*.
- **A service software update AWS has waiting** (patches, often security fixes) shows as *patch pending*.

## MSK clusters

With `WatchMSK=true` (CloudFormation, the default) or `watch_msk = true` (Terraform) the task role may also call
`kafka:ListClustersV2` and `kafka:ListKafkaVersions`, and the agent adds the region's provisioned clusters as an
`msk` target named `msk-<region>` (`GOLIASH_MSK=off` stops it). Serverless clusters have no Kafka version to keep up
and are left out.

- **Each cluster** shows its Kafka version as `kafka.amazonaws.com/kafka:3.6.0`. MSK's `3.7.x` (MSK applies the
  patches itself) reads as 3.7, and its `.kraft` and `.tiered` variants as the Kafka version they run.
- **The versions MSK offers**, and end of support from endoflife.date (`amazon-msk`), work as for the others.
- **Brokers with public access** make the cluster *exposed*.

## Leaving things out

- `goliash.ignore=true` as a tag on an ECS service, Lambda function, RDS database, ElastiCache cache, EKS cluster,
  OpenSearch domain or MSK cluster keeps the agent from reporting it.
- `Clusters` (`GOLIASH_ECS_CLUSTERS`) and `GOLIASH_ECS_EXCLUDE` choose the clusters.
- `WatchLambda`, `WatchRDS`, `WatchElastiCache`, `WatchEKS`, `WatchOpenSearch` and `WatchMSK` set to `false`
  (`GOLIASH_LAMBDA`, `GOLIASH_RDS`, `GOLIASH_ELASTICACHE`, `GOLIASH_EKS`, `GOLIASH_OPENSEARCH`, `GOLIASH_MSK` set to
  `off`) leave that service out.
- In Goliash, an ignore rule (Inbox → **Ignore this image**) drops what the agent still reports.

## Upgrading the stack

**Agents → Update** gives the command. It fetches the template of the new version, so the task role gains what the
new version reads (RDS, ElastiCache, EKS, OpenSearch, MSK, Lambda function URLs), and keeps your parameters.

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
