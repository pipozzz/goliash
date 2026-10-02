# Copyright 2026 The Goliash Authors
# SPDX-License-Identifier: AGPL-3.0-only
#
# Goliash server on Nomad. SQLite lives in the host volume "goliash-data" (declare it in
# the client config); for PostgreSQL put a postgres:// URL in the variable below.
#
#   nomad var put nomad/jobs/goliash database_url=postgres://…   # optional
#   nomad job run goliash-server.nomad.hcl

job "goliash" {
  type = "service"

  group "server" {
    count = 1

    network {
      port "http" { to = 8080 }
    }

    volume "data" {
      type   = "host"
      source = "goliash-data"
    }

    service {
      name     = "goliash"
      port     = "http"
      provider = "nomad"
      check {
        type     = "http"
        path     = "/healthz"
        interval = "15s"
        timeout  = "3s"
      }
    }

    task "goliash" {
      driver = "docker"

      config {
        image = "ghcr.io/pipozzz/goliash:latest"
        ports = ["http"]
      }

      volume_mount {
        volume      = "data"
        destination = "/data"
      }

      env {
        GOLIASH_PUBLIC_URL = "https://goliash.example.com"
      }

      # Optional settings from Nomad Variables (database, SMTP, OIDC).
      template {
        destination = "${NOMAD_SECRETS_DIR}/env"
        env         = true
        data        = <<-EOT
        {{ with nomadVar "nomad/jobs/goliash" }}{{ range .Tuples }}{{ if eq .K "database_url" }}GOLIASH_DATABASE_URL={{ .V }}{{ else }}{{ .K }}={{ .V }}{{ end }}
        {{ end }}{{ end }}
        EOT
      }

      resources {
        cpu    = 200
        memory = 256
      }
    }
  }
}
