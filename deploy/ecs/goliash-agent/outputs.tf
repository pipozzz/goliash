# Copyright 2026 The Goliash Authors
# SPDX-License-Identifier: Apache-2.0

output "task_role_arn" {
  description = "Role the agent runs as (read-only ECS)."
  value       = aws_iam_role.task.arn
}

output "service_name" {
  value = aws_ecs_service.agent.name
}
