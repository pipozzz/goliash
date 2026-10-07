# Copyright 2026 The Goliash Authors
# SPDX-License-Identifier: Apache-2.0
#
# goliash-agent on Nomad. It reads this cluster through the Nomad API with a token
# that has the list-jobs and read-job capabilities (a target's credentials_ref names it).
#
#   nomad acl policy apply goliash-read - <<<'namespace "*" { capabilities = ["list-jobs", "read-job"] }'
#   nomad acl token create -name goliash-agent -policy goliash-read      # copy the secret ID
#   nomad var put nomad/jobs/goliash-agent token=glsh_agent_… nomad_token=<secret ID>
#   nomad job run goliash-agent.nomad.hcl
#
# In Goliash, create a nomad target with credentials_ref "nomad" and settings like
#   {"nomad":{"address":"http://nomad.service.consul:4646"}}

job "goliash-agent" {
  type = "service" # one agent per token; a system job would run one per node

  group "agent" {
    count = 1

    task "agent" {
      driver = "docker"

      config {
        image = "ghcr.io/pipozzz/goliash-agent:latest"
      }

      env {
        GOLIASH_SERVER_URL = "https://goliash.example.com"
        GOLIASH_DATA_DIR   = "${NOMAD_ALLOC_DIR}/data"
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
