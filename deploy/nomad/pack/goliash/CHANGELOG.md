# Changelog

## 0.2.0

- Secrets from the job's Nomad Variable (`nomad/jobs/<job name>`): its `GOLIASH_*` items and `NOMAD_TOKEN` become
  environment variables, so the secret key and tokens stay out of the job specification that every `read-job`
  token shows. The `secret_key`, `github_token` and `nomad_token` variables still work.

## 0.1.0

- Initial pack: Goliash server with SQLite on a volume, bootstrapped owner and environment, and the Nomad cluster
  it runs on watched out of the box.
