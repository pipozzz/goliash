# goliash

[Goliash](https://goliash.dev/) shows what runs where, on which version: a service × environment
matrix with the running and the newest upstream version, history of every deploy, and drift between environments.

Single host-networked Nomad service with a data volume. Out of the box it watches the Nomad cluster it runs on
(read-only), so the matrix fills without an agent. Kubernetes, ECS, Docker hosts and Compose files can be added
in the UI.

## Deploy

```sh
nomad-pack registry add nomploy https://github.com/Nomploy/nomad-packs
nomad-pack run goliash --registry=nomploy \
  --var owner_email=you@example.com --var public_url=https://goliash.example.com
```

Then open the task logs: the first start logs a one-time sign-in link for `owner_email`.

## Configure

| Variable | Default | Description |
| --- | --- | --- |
| `port` | `8070` | UI, API and agent endpoint. |
| `public_url` | `""` | Address people use. Empty = `http://<node-ip>:<port>`. |
| `owner_email` | `admin@example.com` | First owner; a sign-in link is logged until they sign in. |
| `recovery_email` | `""` | Locked out? Set it to your e-mail and redeploy: every start logs a one-time sign-in link. Empty it afterwards. |
| `environment` | `prod` | Environment the Nomad cluster belongs to. |
| `watch_nomad` | `true` | Watch this Nomad cluster without an agent. |
| `nomad_address` | `""` | Nomad API. Empty = `http://<node-ip>:4646`. |
| `nomad_token` | `""` | ACL token with `list-jobs` and `read-job`, when ACLs are on. |
| `secret_key` | `""` | Encrypts channel secrets. Empty = generated on the volume. |
| `github_token` | `""` | Optional, for release notes lookups. |
| `data_volume` | `goliash_data` | `/data` — SQLite database and secret key. |
| `image` | `ghcr.io/pipozzz/goliash:1.17.3` | Image. Pin a tag in production. |
| `resources` | `{ cpu = 200, memory = 256 }` | Task resources. |

## Notes

- **Single node.** State is SQLite on a local volume: `count = 1`; pin the job with `constraints`. For more,
  point `GOLIASH_DATABASE_URL` at PostgreSQL (see the docs).
- **ACLs.** With Nomad ACLs on, create a read-only token and pass it as `nomad_token`:
  `nomad acl policy apply goliash-read - <<<'namespace "*" { capabilities = ["list-jobs", "read-job"] }'` then
  `nomad acl token create -name goliash -policy goliash-read`.
- **Mapping.** Add `goliash.service` to a job's `meta` to name its service; everything else waits in the Inbox.
- **Backups.** Back up the volume: the database and `goliash.key`, which channel secrets need.
- **TLS.** Plain HTTP; put it behind Traefik with TLS before exposing it.
