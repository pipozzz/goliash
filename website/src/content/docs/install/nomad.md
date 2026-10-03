---
title: Nomad
---

## Server

`deploy/nomad/goliash-server.nomad.hcl` runs the server. SQLite lives in the host volume `goliash-data`, which
you declare in the client configuration. For PostgreSQL, put a URL in the job's variable:

```sh
nomad var put nomad/jobs/goliash database_url=postgres://…   # optional
nomad job run deploy/nomad/goliash-server.nomad.hcl
```

## Agent

The agent reads the cluster through the Nomad API with a token that has the `read-job` capability:

```sh
nomad acl policy apply goliash-read - <<<'namespace "*" { capabilities = ["read-job"] }'
nomad acl token create -name goliash-agent -policy goliash-read      # copy the secret ID
nomad var put nomad/jobs/goliash-agent token=glsh_agent_… nomad_token=<secret ID>
nomad job run deploy/nomad/goliash-agent.nomad.hcl
```

Edit `GOLIASH_SERVER_URL` in the job first. Create a `nomad` target with credentials reference `nomad` and
settings like `{"nomad":{"address":"http://nomad.service.consul:4646"}}`. The job passes the Nomad token to the
agent as `GOLIASH_CREDENTIAL_NOMAD`.

Run one agent per token: the job is a `service` job with one instance, not a `system` job.

## What the Nomad collector reports

One workload per job, with a container per task (`group/task`) and its image, counted by running allocations.
During a deployment the old and new job versions show side by side. Optional settings: `region` and `namespaces`
(empty means every namespace the token can read).
