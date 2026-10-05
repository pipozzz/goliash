---
title: Security
description: 'How Goliash stays read-only, protects tokens, sessions and secrets, and how to verify signed releases.'
---

## Read-only by design

- **Collectors only read.** The Kubernetes ClusterRole allows `get`, `list` and `watch`; the ECS role
  `ecs:List*`, `ecs:Describe*` and `ecr:ListImages`; Nomad needs `list-jobs` and `read-job`; Docker and Swarm go through a
  docker-socket-proxy that allows only `GET`. Nothing in Goliash creates, changes or deletes anything in your
  infrastructure.
- **The agent only connects out.** It sends snapshots to the server over HTTPS. The server never connects to the
  agent, so no inbound port is needed in your network.
- **Credentials stay with the agent.** The server sends only the name of a credential; the agent resolves it from
  its own environment or files.

## Tokens and sessions

- Agent tokens (`glsh_agent_…`) and API tokens (`glsh_api_…`) are shown once and stored as SHA-256 hashes. They
  carry a checksum, so secret scanners can recognize them. An agent token can only send data and read that agent's
  configuration.
- Passwords are hashed with argon2id (19 MiB, 2 passes) and must have at least 12 characters. Failed sign-ins are
  limited per address and per client, answer the same whether or not the account exists, and take the same time.
- Two-factor sign-in with TOTP codes (RFC 6238) is available to everyone and also guards sign-in links; codes
  cannot be replayed, recovery codes are single-use and stored hashed, the secret is encrypted at rest.
- Owners can require two-factor sign-in for everyone who does not use single sign-on.
- Sessions end after 30 days, or 14 days unused (both configurable). Request bodies are capped at 1 MB outside the
  agent protocol, which has its own limits.
- Sign-in links are single-use and short-lived. Sessions are HttpOnly cookies, marked Secure when
  `GOLIASH_PUBLIC_URL` uses `https://`. Browsers may not send state-changing requests from other origins.
- OIDC sign-in uses PKCE, state and nonce.
- Every response carries a strict Content-Security-Policy (only the server's own scripts, no inline script, no
  framing), `X-Frame-Options: DENY`, `X-Content-Type-Options: nosniff`, `Referrer-Policy: same-origin` and, when
  `GOLIASH_PUBLIC_URL` uses `https://`, `Strict-Transport-Security`.

## Secrets at rest

Notification channel secrets are encrypted with AES-256-GCM and bound to their channel; see
[Install](/install/#secrets-at-rest). Webhook deliveries can be signed with HMAC-SHA256; see
[Notifications](/guide/notifications/#webhook-payload).

## Audit log

Every change to configuration, tokens and roles, and every sign-in, is recorded with who made it. Secrets never
appear in the log.

## Verify a release

Images and the release checksums are signed with [cosign](https://github.com/sigstore/cosign) in the release
workflow, without long-lived keys:

```sh
cosign verify ghcr.io/pipozzz/goliash:0.1.0 \
  --certificate-identity-regexp '^https://github.com/pipozzz/goliash/' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
```

For binaries, download `checksums.txt` and `checksums.txt.sigstore.json` from the release, then:

```sh
cosign verify-blob checksums.txt --bundle checksums.txt.sigstore.json \
  --certificate-identity-regexp '^https://github.com/pipozzz/goliash/' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
sha256sum --ignore-missing -c checksums.txt        # macOS: shasum -a 256 --ignore-missing -c checksums.txt
```

Each image and archive comes with an SPDX SBOM.

## Reporting a vulnerability

Please report vulnerabilities privately through
[GitHub security advisories](https://github.com/pipozzz/goliash/security/advisories/new), not in public issues.
