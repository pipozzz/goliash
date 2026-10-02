-- Copyright 2026 The Goliash Authors
-- SPDX-License-Identifier: AGPL-3.0-only

-- +goose Up
-- Tenancy. Self-hosted runs with one implicit organization.
CREATE TABLE organizations (
    id          TEXT PRIMARY KEY,
    name        TEXT NOT NULL,
    plan        TEXT NOT NULL DEFAULT 'oss',
    created_at  TIMESTAMPTZ NOT NULL
);

CREATE TABLE workspaces (
    id          TEXT PRIMARY KEY,
    org_id      TEXT NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    name        TEXT NOT NULL,
    slug        TEXT NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL,
    UNIQUE (org_id, slug)
);

-- Every table below carries org_id and workspace_id (PostgreSQL row level security keys on them).

CREATE TABLE environments (
    id            TEXT PRIMARY KEY,
    org_id        TEXT NOT NULL,
    workspace_id  TEXT NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    name          TEXT NOT NULL,
    position      INTEGER NOT NULL DEFAULT 0, -- promotion order: dev < staging < prod
    created_at    TIMESTAMPTZ NOT NULL,
    UNIQUE (workspace_id, name)
);

CREATE TABLE agents (
    id             TEXT PRIMARY KEY,
    org_id         TEXT NOT NULL,
    workspace_id   TEXT NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    name           TEXT NOT NULL,
    version        TEXT NOT NULL DEFAULT '',
    hostname       TEXT NOT NULL DEFAULT '',
    platforms      JSONB NOT NULL DEFAULT '[]',
    registered_at  TIMESTAMPTZ,
    last_seen_at   TIMESTAMPTZ,
    created_at     TIMESTAMPTZ NOT NULL,
    UNIQUE (workspace_id, name)
);

-- Machine tokens (glsh_agent_, glsh_ci_, glsh_api_). Only a SHA-256 hash is stored.
CREATE TABLE tokens (
    id            TEXT PRIMARY KEY,
    org_id        TEXT NOT NULL,
    workspace_id  TEXT NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    kind          TEXT NOT NULL CHECK (kind IN ('agent', 'ci', 'api')),
    name          TEXT NOT NULL,
    hash          TEXT NOT NULL UNIQUE,
    agent_id      TEXT REFERENCES agents (id) ON DELETE CASCADE,
    created_at    TIMESTAMPTZ NOT NULL,
    last_used_at  TIMESTAMPTZ,
    revoked_at    TIMESTAMPTZ,
    CHECK ((kind = 'agent') = (agent_id IS NOT NULL))
);

-- A Kubernetes cluster, ECS cluster, Nomad region or Swarm cluster. agent_id NULL = collected by the server itself.
CREATE TABLE targets (
    id                     TEXT PRIMARY KEY,
    org_id                 TEXT NOT NULL,
    workspace_id           TEXT NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    environment_id         TEXT NOT NULL REFERENCES environments (id),
    agent_id               TEXT REFERENCES agents (id) ON DELETE SET NULL,
    platform               TEXT NOT NULL CHECK (platform IN ('kubernetes', 'ecs', 'nomad', 'swarm')),
    name                   TEXT NOT NULL,
    settings               JSONB NOT NULL DEFAULT '{}',
    poll_interval_seconds  INTEGER NOT NULL DEFAULT 300,
    last_snapshot_at       TIMESTAMPTZ,
    created_at             TIMESTAMPTZ NOT NULL,
    UNIQUE (workspace_id, name)
);
CREATE INDEX targets_agent_idx ON targets (agent_id);

-- Raw snapshots as received; snapshot_id from the agent makes ingestion idempotent.
CREATE TABLE snapshots (
    id            TEXT PRIMARY KEY,
    org_id        TEXT NOT NULL,
    workspace_id  TEXT NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    target_id     TEXT NOT NULL REFERENCES targets (id) ON DELETE CASCADE,
    agent_id      TEXT REFERENCES agents (id) ON DELETE SET NULL,
    collected_at  TIMESTAMPTZ NOT NULL,
    received_at   TIMESTAMPTZ NOT NULL,
    complete      BOOLEAN NOT NULL,
    payload       JSONB NOT NULL,
    processed_at  TIMESTAMPTZ
);
CREATE INDEX snapshots_target_collected_idx ON snapshots (target_id, collected_at);
CREATE INDEX snapshots_unprocessed_idx ON snapshots (received_at) WHERE processed_at IS NULL;

CREATE TABLE services (
    id              TEXT PRIMARY KEY,
    org_id          TEXT NOT NULL,
    workspace_id    TEXT NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    name            TEXT NOT NULL,
    owner           TEXT NOT NULL DEFAULT '',
    kind            TEXT NOT NULL DEFAULT 'own' CHECK (kind IN ('own', 'third_party')),
    upstream        TEXT NOT NULL DEFAULT '', -- image repository or source repo releases are read from
    version_policy  JSONB NOT NULL DEFAULT '{}', -- tag_filter, track, pin_major, prerelease
    created_at      TIMESTAMPTZ NOT NULL,
    UNIQUE (workspace_id, name)
);

-- One observed running container (image + digest) of a workload on a target. service_id NULL = unmapped (inbox).
CREATE TABLE instances (
    id              TEXT PRIMARY KEY,
    org_id          TEXT NOT NULL,
    workspace_id    TEXT NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    target_id       TEXT NOT NULL REFERENCES targets (id) ON DELETE CASCADE,
    service_id      TEXT REFERENCES services (id) ON DELETE SET NULL,
    workload_id     TEXT NOT NULL,
    workload_kind   TEXT NOT NULL,
    namespace       TEXT NOT NULL DEFAULT '',
    workload_name   TEXT NOT NULL,
    container_name  TEXT NOT NULL,
    image           TEXT NOT NULL,
    tag             TEXT NOT NULL DEFAULT '',
    digest          TEXT NOT NULL DEFAULT '',
    running         INTEGER NOT NULL DEFAULT 0,
    is_main         BOOLEAN NOT NULL DEFAULT FALSE,
    first_seen_at   TIMESTAMPTZ NOT NULL,
    last_seen_at    TIMESTAMPTZ NOT NULL,
    removed_at      TIMESTAMPTZ,
    UNIQUE (target_id, workload_id, container_name, image, digest)
);
CREATE INDEX instances_service_idx ON instances (service_id);
CREATE INDEX instances_workspace_unmapped_idx ON instances (workspace_id) WHERE service_id IS NULL;

-- Upstream versions of a service, from registries or GitHub/GitLab releases.
CREATE TABLE releases (
    id             TEXT PRIMARY KEY,
    org_id         TEXT NOT NULL,
    workspace_id   TEXT NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    service_id     TEXT NOT NULL REFERENCES services (id) ON DELETE CASCADE,
    version        TEXT NOT NULL,
    digest         TEXT NOT NULL DEFAULT '',
    published_at   TIMESTAMPTZ,
    changelog_url  TEXT NOT NULL DEFAULT '',
    discovered_at  TIMESTAMPTZ NOT NULL,
    UNIQUE (service_id, version)
);

-- History. deployed, version_changed, removed, new_release, drift_detected, drift_resolved.
CREATE TABLE events (
    id              TEXT PRIMARY KEY,
    org_id          TEXT NOT NULL,
    workspace_id    TEXT NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    type            TEXT NOT NULL CHECK (type IN ('deployed', 'version_changed', 'removed', 'new_release', 'drift_detected', 'drift_resolved')),
    service_id      TEXT REFERENCES services (id) ON DELETE SET NULL,
    environment_id  TEXT REFERENCES environments (id) ON DELETE SET NULL,
    target_id       TEXT REFERENCES targets (id) ON DELETE SET NULL,
    instance_id     TEXT REFERENCES instances (id) ON DELETE SET NULL,
    from_version    TEXT NOT NULL DEFAULT '',
    to_version      TEXT NOT NULL DEFAULT '',
    actor           TEXT NOT NULL DEFAULT '',
    source          TEXT NOT NULL DEFAULT 'poll' CHECK (source IN ('poll', 'ci', 'git')),
    note            TEXT NOT NULL DEFAULT '', -- e.g. "retag" when the digest changed under the same tag
    at              TIMESTAMPTZ NOT NULL
);
CREATE INDEX events_workspace_at_idx ON events (workspace_id, at);
CREATE INDEX events_service_at_idx ON events (service_id, at);

-- Mapping rules created from config or from classifying inbox instances.
CREATE TABLE mapping_rules (
    id              TEXT PRIMARY KEY,
    org_id          TEXT NOT NULL,
    workspace_id    TEXT NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    priority        INTEGER NOT NULL DEFAULT 0,
    match_type      TEXT NOT NULL CHECK (match_type IN ('image_repo', 'workload_name', 'label', 'ignore')),
    pattern         TEXT NOT NULL,
    service_id      TEXT REFERENCES services (id) ON DELETE CASCADE,
    created_at      TIMESTAMPTZ NOT NULL
);
CREATE INDEX mapping_rules_workspace_idx ON mapping_rules (workspace_id, priority);

CREATE TABLE notification_channels (
    id            TEXT PRIMARY KEY,
    org_id        TEXT NOT NULL,
    workspace_id  TEXT NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    type          TEXT NOT NULL CHECK (type IN ('slack', 'webhook', 'email')),
    name          TEXT NOT NULL,
    config        JSONB NOT NULL DEFAULT '{}',
    created_at    TIMESTAMPTZ NOT NULL,
    UNIQUE (workspace_id, name)
);

CREATE TABLE notification_rules (
    id            TEXT PRIMARY KEY,
    org_id        TEXT NOT NULL,
    workspace_id  TEXT NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    channel_id    TEXT NOT NULL REFERENCES notification_channels (id) ON DELETE CASCADE,
    event_types   JSONB NOT NULL DEFAULT '[]',
    filter        JSONB NOT NULL DEFAULT '{}', -- service, owner, environment, min semver jump
    mode          TEXT NOT NULL DEFAULT 'instant' CHECK (mode IN ('instant', 'daily', 'weekly')),
    created_at    TIMESTAMPTZ NOT NULL
);

-- "We know about 2.0, quiet until 2.1" or "quiet for 14 days".
CREATE TABLE acks (
    id                TEXT PRIMARY KEY,
    org_id            TEXT NOT NULL,
    workspace_id      TEXT NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    service_id        TEXT NOT NULL REFERENCES services (id) ON DELETE CASCADE,
    environment_id    TEXT REFERENCES environments (id) ON DELETE CASCADE,
    kind              TEXT NOT NULL CHECK (kind IN ('release', 'drift')),
    until_version     TEXT NOT NULL DEFAULT '',
    until_at          TIMESTAMPTZ,
    created_by        TEXT NOT NULL DEFAULT '',
    created_at        TIMESTAMPTZ NOT NULL
);
CREATE INDEX acks_service_idx ON acks (service_id);

CREATE TABLE audit_log (
    id            TEXT PRIMARY KEY,
    org_id        TEXT NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    workspace_id  TEXT,
    actor         TEXT NOT NULL,
    action        TEXT NOT NULL,
    details       JSONB NOT NULL DEFAULT '{}',
    at            TIMESTAMPTZ NOT NULL
);
CREATE INDEX audit_log_org_at_idx ON audit_log (org_id, at);

-- +goose Down
DROP TABLE IF EXISTS audit_log;
DROP TABLE IF EXISTS acks;
DROP TABLE IF EXISTS notification_rules;
DROP TABLE IF EXISTS notification_channels;
DROP TABLE IF EXISTS mapping_rules;
DROP TABLE IF EXISTS events;
DROP TABLE IF EXISTS releases;
DROP TABLE IF EXISTS instances;
DROP TABLE IF EXISTS services;
DROP TABLE IF EXISTS snapshots;
DROP TABLE IF EXISTS targets;
DROP TABLE IF EXISTS tokens;
DROP TABLE IF EXISTS agents;
DROP TABLE IF EXISTS environments;
DROP TABLE IF EXISTS workspaces;
DROP TABLE IF EXISTS organizations;
