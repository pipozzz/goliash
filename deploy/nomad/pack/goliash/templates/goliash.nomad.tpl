job "[[ var "job_name" . ]]" {
  namespace   = "[[ var "namespace" . ]]"
  datacenters = [[ var "datacenters" . | toStringList ]]
  type        = "service"

  [[- range $c := var "constraints" . ]]
  constraint {
    attribute = "[[ $c.attribute ]]"
    operator  = "[[ $c.operator ]]"
    value     = "[[ $c.value ]]"
  }
  [[- end ]]

  group "[[ var "job_name" . ]]" {
    count = 1

    network {
      mode = "host"
      port "http" {
        static = [[ var "port" . ]]
      }
    }

    service {
      name     = "[[ var "job_name" . ]]"
      provider = "nomad"
      port     = "http"

      check {
        type     = "http"
        path     = "/healthz"
        interval = "30s"
        timeout  = "5s"
      }
    }

    restart {
      attempts = 3
      interval = "5m"
      delay    = "15s"
      mode     = "delay"
    }

    task "goliash" {
      driver = "docker"

      config {
        image        = "[[ var "image" . ]]"
        network_mode = "host"
        ports        = ["http"]
        args         = ["serve"]
        mount {
          type   = "volume"
          target = "/data"
          source = "[[ var "data_volume" . ]]"
        }
      }

      env {
        GOLIASH_LISTEN         = ":[[ var "port" . ]]"
        GOLIASH_BOOTSTRAP_FILE = "/local/bootstrap.yaml"
        [[- if ne (var "public_url" .) "" ]]
        GOLIASH_PUBLIC_URL = "[[ var "public_url" . ]]"
        [[- else ]]
        GOLIASH_PUBLIC_URL = "http://$${attr.unique.network.ip-address}:[[ var "port" . ]]"
        [[- end ]]
        [[- if ne (var "secret_key" .) "" ]]
        GOLIASH_SECRET_KEY = "[[ var "secret_key" . ]]"
        [[- end ]]
        [[- if ne (var "github_token" .) "" ]]
        GOLIASH_GITHUB_TOKEN = "[[ var "github_token" . ]]"
        [[- end ]]
        [[- if ne (var "nomad_token" .) "" ]]
        GOLIASH_CREDENTIAL_NOMAD = "[[ var "nomad_token" . ]]"
        [[- end ]]
        [[- if ne (var "recovery_email" .) "" ]]
        GOLIASH_RECOVERY_EMAIL = "[[ var "recovery_email" . ]]"
        [[- end ]]
      }

      # Created on first start only: changes made later in the UI are kept.
      template {
        destination = "local/bootstrap.yaml"
        data        = <<EOH
owner: "[[ var "owner_email" . ]]"
environments:
  - name: "[[ var "environment" . ]]"
    position: 30
[[- if var "watch_nomad" . ]]
targets:
  - name: "[[ var "job_name" . ]]-nomad"
    environment: "[[ var "environment" . ]]"
    platform: nomad
    poll_interval_seconds: 60
    settings:
[[- if ne (var "nomad_token" .) "" ]]
      credentials_ref: nomad
[[- end ]]
      nomad:
[[- if ne (var "nomad_address" .) "" ]]
        address: "[[ var "nomad_address" . ]]"
[[- else ]]
        address: "http://{{ env "attr.unique.network.ip-address" }}:4646"
[[- end ]]
[[- end ]]
EOH
      }

      resources {
        cpu    = [[ (var "resources" .).cpu ]]
        memory = [[ (var "resources" .).memory ]]
      }
    }
  }
}
