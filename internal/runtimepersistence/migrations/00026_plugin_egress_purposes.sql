-- +goose Up
-- Plugin catalog discovery and artifact download are separate core-owned
-- runtime egress purposes. SQLite cannot widen the purpose CHECK in place, so
-- rebuild the table while preserving every durable column and sequence.
ALTER TABLE runtime_egress_attempts
RENAME TO runtime_egress_attempts_revision_25;

DROP INDEX runtime_egress_attempts_latest;
DROP INDEX runtime_egress_attempts_by_connection;
DROP INDEX runtime_egress_attempts_by_parent;
DROP INDEX runtime_egress_attempts_by_exchange;

CREATE TABLE runtime_egress_attempts (
    sequence INTEGER PRIMARY KEY AUTOINCREMENT,
    attempt_id TEXT NOT NULL UNIQUE
        CHECK (length(CAST(attempt_id AS BLOB)) BETWEEN 1 AND 512),
    connection_id TEXT NOT NULL DEFAULT ''
        CHECK (length(CAST(connection_id AS BLOB)) <= 512),
    purpose TEXT NOT NULL
        CHECK (purpose IN (
            'provider_attempt',
            'profile_operation',
            'original_origin',
            'agent_probe',
            'blind_tunnel',
            'auxiliary_llm',
            'language_transform',
            'plugin_catalog_sync',
            'plugin_artifact_fetch',
            'update'
        )),
    -- No unknown member: an unclassified operation never produces an outbound
    -- attempt, so a row claiming one would be a contradiction.
    payload_class TEXT NOT NULL
        CHECK (payload_class IN (
            'none',
            'control',
            'client_data',
            'client_semantic',
            'opaque_tunnel',
            'runtime'
        )),
    parent_kind TEXT NOT NULL
        CHECK (parent_kind IN (
            'upstream_attempt',
            'client_operation',
            'original_request',
            'blind_connection',
            'runtime_action'
        )),
    parent_id TEXT NOT NULL
        CHECK (length(CAST(parent_id AS BLOB)) BETWEEN 1 AND 512),
    parent_exchange_id TEXT NOT NULL DEFAULT ''
        CHECK (length(CAST(parent_exchange_id AS BLOB)) <= 512),
    caller_kind TEXT NOT NULL
        CHECK (caller_kind IN ('core', 'plugin')),
    caller_id TEXT NOT NULL DEFAULT ''
        CHECK (length(CAST(caller_id AS BLOB)) <= 512),
    target_origin TEXT NOT NULL
        CHECK (length(CAST(target_origin AS BLOB)) BETWEEN 1 AND 1024),
    policy_id TEXT NOT NULL
        CHECK (length(CAST(policy_id AS BLOB)) BETWEEN 1 AND 512),
    policy_revision INTEGER NOT NULL
        CHECK (policy_revision > 0),
    policy_authority TEXT NOT NULL
        CHECK (policy_authority IN ('access', 'network', 'runtime')),
    rule_id TEXT NOT NULL
        CHECK (length(CAST(rule_id AS BLOB)) BETWEEN 1 AND 512),
    proxy_id TEXT NOT NULL
        CHECK (length(CAST(proxy_id AS BLOB)) BETWEEN 1 AND 512),
    reused_transport INTEGER NOT NULL DEFAULT 0
        CHECK (reused_transport IN (0, 1)),
    started_at_unix_ms INTEGER NOT NULL,
    completed_at_unix_ms INTEGER,
    outcome TEXT NOT NULL DEFAULT ''
        CHECK (outcome IN ('', 'completed', 'failed', 'canceled')),
    error_class TEXT NOT NULL DEFAULT ''
        CHECK (length(CAST(error_class AS BLOB)) <= 512),
    bytes_out INTEGER NOT NULL DEFAULT 0
        CHECK (bytes_out >= 0),
    bytes_in INTEGER NOT NULL DEFAULT 0
        CHECK (bytes_in >= 0)
) STRICT;

INSERT INTO runtime_egress_attempts (
    sequence,
    attempt_id,
    connection_id,
    purpose,
    payload_class,
    parent_kind,
    parent_id,
    parent_exchange_id,
    caller_kind,
    caller_id,
    target_origin,
    policy_id,
    policy_revision,
    policy_authority,
    rule_id,
    proxy_id,
    reused_transport,
    started_at_unix_ms,
    completed_at_unix_ms,
    outcome,
    error_class,
    bytes_out,
    bytes_in
)
SELECT
    sequence,
    attempt_id,
    connection_id,
    purpose,
    payload_class,
    parent_kind,
    parent_id,
    parent_exchange_id,
    caller_kind,
    caller_id,
    target_origin,
    policy_id,
    policy_revision,
    policy_authority,
    rule_id,
    proxy_id,
    reused_transport,
    started_at_unix_ms,
    completed_at_unix_ms,
    outcome,
    error_class,
    bytes_out,
    bytes_in
FROM runtime_egress_attempts_revision_25;

DROP TABLE runtime_egress_attempts_revision_25;

CREATE INDEX runtime_egress_attempts_latest
    ON runtime_egress_attempts (sequence DESC);

CREATE INDEX runtime_egress_attempts_by_connection
    ON runtime_egress_attempts (connection_id, sequence);

CREATE INDEX runtime_egress_attempts_by_parent
    ON runtime_egress_attempts (parent_kind, parent_id, sequence);

CREATE INDEX runtime_egress_attempts_by_exchange
    ON runtime_egress_attempts (parent_exchange_id, sequence DESC)
    WHERE parent_exchange_id <> '';
