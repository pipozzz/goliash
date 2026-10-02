# Copyright 2026 The Goliash Authors
# SPDX-License-Identifier: Apache-2.0
#
# goliash-agent as an ECS Fargate service. Its task role may only call ecs:List* and
# ecs:Describe*, so it reads every cluster in the region and changes nothing.

terraform {
  required_version = ">= 1.5"
  required_providers {
    aws = { source = "hashicorp/aws", version = ">= 6.0" }
  }
}

data "aws_region" "current" {}

resource "aws_cloudwatch_log_group" "agent" {
  name              = "/ecs/${var.name}"
  retention_in_days = var.log_retention_days
  tags              = var.tags
}

data "aws_iam_policy_document" "assume" {
  statement {
    actions = ["sts:AssumeRole"]
    principals {
      type        = "Service"
      identifiers = ["ecs-tasks.amazonaws.com"]
    }
  }
}

# Task role: what the agent itself may do. Read-only ECS.
resource "aws_iam_role" "task" {
  name               = "${var.name}-task"
  assume_role_policy = data.aws_iam_policy_document.assume.json
  tags               = var.tags
}

data "aws_iam_policy_document" "read_ecs" {
  statement {
    sid       = "ReadOnlyEcs"
    actions   = ["ecs:List*", "ecs:Describe*"]
    resources = ["*"]
  }
}

resource "aws_iam_role_policy" "read_ecs" {
  name   = "goliash-read-ecs"
  role   = aws_iam_role.task.id
  policy = data.aws_iam_policy_document.read_ecs.json
}

# Execution role: lets ECS pull the image, write logs and read the token secret.
resource "aws_iam_role" "execution" {
  name               = "${var.name}-execution"
  assume_role_policy = data.aws_iam_policy_document.assume.json
  tags               = var.tags
}

resource "aws_iam_role_policy_attachment" "execution" {
  role       = aws_iam_role.execution.name
  policy_arn = "arn:aws:iam::aws:policy/service-role/AmazonECSTaskExecutionRolePolicy"
}

data "aws_iam_policy_document" "token" {
  statement {
    actions   = ["secretsmanager:GetSecretValue"]
    resources = [var.token_secret_arn]
  }
}

resource "aws_iam_role_policy" "token" {
  name   = "goliash-agent-token"
  role   = aws_iam_role.execution.id
  policy = data.aws_iam_policy_document.token.json
}

resource "aws_ecs_task_definition" "agent" {
  family                   = var.name
  requires_compatibilities = ["FARGATE"]
  network_mode             = "awsvpc"
  cpu                      = 256
  memory                   = 512
  task_role_arn            = aws_iam_role.task.arn
  execution_role_arn       = aws_iam_role.execution.arn
  runtime_platform {
    operating_system_family = "LINUX"
    cpu_architecture        = var.cpu_architecture
  }
  container_definitions = jsonencode([{
    name      = "agent"
    image     = var.image
    essential = true
    environment = [
      { name = "GOLIASH_SERVER_URL", value = var.server_url },
      { name = "GOLIASH_DATA_DIR", value = "/tmp/goliash" },
    ]
    secrets                = [{ name = "GOLIASH_AGENT_TOKEN", valueFrom = var.token_secret_arn }]
    readonlyRootFilesystem = false
    logConfiguration = {
      logDriver = "awslogs"
      options = {
        awslogs-group         = aws_cloudwatch_log_group.agent.name
        awslogs-region        = data.aws_region.current.region
        awslogs-stream-prefix = "agent"
      }
    }
  }])
  tags = var.tags
}

resource "aws_ecs_service" "agent" {
  name            = var.name
  cluster         = var.cluster_arn
  task_definition = aws_ecs_task_definition.agent.arn
  desired_count   = 1 # one agent per token
  launch_type     = "FARGATE"

  deployment_minimum_healthy_percent = 0 # never two agents with the same token
  deployment_maximum_percent         = 100

  network_configuration {
    subnets          = var.subnet_ids
    security_groups  = var.security_group_ids
    assign_public_ip = var.assign_public_ip
  }
  tags = var.tags
}
