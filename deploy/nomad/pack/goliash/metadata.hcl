app {
  url = "https://goliash.dev/"
}

pack {
  name        = "goliash"
  description = "Goliash — what runs where, on which version: a service × environment matrix with upstream releases and drift. Deployed as a single host-networked Nomad service with a data volume; it watches the Nomad cluster it runs on out of the box (read-only) and can add Kubernetes, ECS, Docker and Compose targets."
  url         = "https://github.com/Nomploy/nomad-packs/tree/main/packs/goliash"
  version     = "0.1.0"
}
