---
title: Nomad
description: 'Run Goliash on Nomad with a job or the Nomad pack, and watch jobs with a list-jobs and read-job ACL token.'
---

## One command with nomad-pack (Nomploy)

The `goliash` pack runs the server with SQLite on a volume and watches the Nomad cluster it runs on, read-only,
without an agent. The matrix fills on its own:

```sh
nomad-pack registry add nomploy https://github.com/Nomploy/nomad-packs
nomad-pack run goliash --registry=nomploy \
  --var owner_email=you@example.com --var public_url=https://goliash.example.com
```

The first start logs a one-time sign-in link for `owner_email` in the task logs. With Nomad ACLs on, pass a token
with `list-jobs` and `read-job` as `nomad_token`. All variables are in the
[pack README](https://github.com/pipozzz/goliash/tree/main/deploy/nomad/pack/goliash).

## Server

`deploy/nomad/goliash-server.nomad.hcl` runs the server. SQLite lives in the host volume `goliash-data`, which
you declare in the client configuration. For PostgreSQL, put a URL in the job's variable:

```sh
nomad var put nomad/jobs/goliash database_url=postgres://…   # optional
nomad job run deploy/nomad/goliash-server.nomad.hcl
```

## Agent

The agent reads the cluster through the Nomad API with a token that has the `list-jobs` and `read-job` capabilities:

```sh
nomad acl policy apply goliash-read - <<<'namespace "*" { capabilities = ["list-jobs", "read-job"] }'
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
