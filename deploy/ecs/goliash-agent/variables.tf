# Copyright 2026 The Goliash Authors
# SPDX-License-Identifier: Apache-2.0

variable "name" {
  description = "Name for the service, roles and log group."
  type        = string
  default     = "goliash-agent"
}

variable "cluster_arn" {
  description = "ECS cluster the agent runs in (it can read every cluster in the region)."
  type        = string
}

variable "server_url" {
  description = "Goliash server URL, reachable from the subnets (outbound HTTPS)."
  type        = string
}

variable "token_secret_arn" {
  description = "Secrets Manager secret holding the enrollment code from Connect an agent (glsh_enroll_…), or an agent token (glsh_agent_…), as a plain string."
  type        = string
}

variable "subnet_ids" {
  description = "Subnets with outbound internet or a route to the server."
  type        = list(string)
}

variable "security_group_ids" {
  description = "Security groups; the agent needs outbound HTTPS only, no inbound rules."
  type        = list(string)
}

variable "assign_public_ip" {
  description = "Set true in public subnets without a NAT gateway."
  type        = bool
  default     = false
}

variable "image" {
  description = "Agent image."
  type        = string
  default     = "ghcr.io/pipozzz/goliash-agent:latest"
}

variable "cpu_architecture" {
  description = "X86_64 or ARM64."
  type        = string
  default     = "ARM64"
}

variable "log_retention_days" {
  type    = number
  default = 30
}

variable "tags" {
  type    = map(string)
  default = {}
}
