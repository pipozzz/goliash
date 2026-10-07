# Copyright 2026 The Goliash Authors
# SPDX-License-Identifier: Apache-2.0
#
# goliash-agent on Nomad. It reads this cluster through the Nomad API with a token
# that has the list-jobs and read-job capabilities (a target's credentials_ref names it).
#
#   nomad acl policy apply goliash-read - <<<'namespace "*" { capabilities = ["list-jobs", "read-job"] }'
#   nomad acl token create -name goliash-agent -policy goliash-read      # copy the secret ID
#   nomad var put nomad/jobs/goliash-agent token=glsh_enroll_… nomad_token=<secret ID>
#   nomad job run -var server_url=https://goliash.example.com goliash-agent.nomad.hcl
#
# token is the code from Connect an agent: the agent registers itself and adds this
# region as a target, read through the Nomad agent on its node (NOMAD_ADDR below).
# With an agent token (glsh_agent_…) instead, create a nomad target in Goliash with
# credentials_ref "nomad" and settings like
#   {"nomad":{"address":"http://nomad.service.consul:4646"}}

variable "server_url" {
  description = "Goliash server URL, reachable from the cluster."
  type        = string
}

variable "version" {
  description = "Agent image tag; the server's release fits best."
  type        = string
  default     = "latest"
}

job "goliash-agent" {
  type = "service" # one agent per token; a system job would run one per node

  group "agent" {
    count = 1

    task "agent" {
      driver = "docker"

      config {
        image = "ghcr.io/pipozzz/goliash-agent:${var.version}"
      }

      env {
        GOLIASH_SERVER_URL = var.server_url
        GOLIASH_DATA_DIR   = "${NOMAD_ALLOC_DIR}/data"
        NOMAD_ADDR         = "http://${attr.unique.network.ip-address}:4646"
      }

      template {
        destination = "${NOMAD_SECRETS_DIR}/env"
        env         = true
        data        = <<-EOT
        {{ with nomadVar "nomad/jobs/goliash-agent" }}
        GOLIASH_AGENT_TOKEN={{ .token }}
        {{ with .nomad_token }}GOLIASH_CREDENTIAL_NOMAD={{ . }}{{ end }}
        {{ end }}
        EOT
      }

      resources {
        cpu    = 100
        memory = 128
      }
    }
  }
}
