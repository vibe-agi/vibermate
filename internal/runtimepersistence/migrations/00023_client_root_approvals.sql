-- +goose Up
-- A client whose publisher is recognized but whose exact build is not in the
-- release catalog needs an explicit, per-launch Root decision. The Go
-- approval contract already models that question as client_root_ask, but the
-- durable kind constraint still admits only tool and network questions.
--
-- SQLite cannot widen a CHECK constraint in place, so rebuild the table. No
-- existing row changes meaning; the new kind has the same request-only scope
-- and absent network target as any other non-network question.
ALTER TABLE tool_approvals RENAME TO tool_approvals_without_client_root;

DROP INDEX tool_approvals_decision_idempotency;
DROP INDEX tool_approvals_state_created;
DROP INDEX tool_approvals_pending_aggregate;

CREATE TABLE tool_approvals (
    approval_id TEXT PRIMARY KEY NOT NULL
        CHECK (length(CAST(approval_id AS BLOB)) BETWEEN 1 AND 512),
    revision INTEGER NOT NULL
        CHECK (revision BETWEEN 1 AND 9223372036854775807),
    kind TEXT NOT NULL
        CHECK (kind IN ('tool_intent', 'network_ask', 'client_root_ask')),
    exchange_id TEXT NOT NULL DEFAULT ''
        CHECK (length(CAST(exchange_id AS BLOB)) <= 512),
    access_id TEXT NOT NULL DEFAULT ''
        CHECK (length(CAST(access_id AS BLOB)) <= 128),
    plan_revision INTEGER NOT NULL DEFAULT 0
        CHECK (plan_revision BETWEEN 0 AND 9223372036854775807),
    plan_hash BLOB NOT NULL DEFAULT x''
        CHECK (length(plan_hash) IN (0, 32)),
    subject_refs_json BLOB NOT NULL
        CHECK (length(subject_refs_json) BETWEEN 3 AND 65536),
    subject_labels_json BLOB NOT NULL
        CHECK (length(subject_labels_json) BETWEEN 3 AND 65536),
    target_host TEXT NOT NULL DEFAULT ''
        CHECK (length(CAST(target_host AS BLOB)) <= 253),
    target_port INTEGER NOT NULL DEFAULT 0
        CHECK (target_port BETWEEN 0 AND 65535),
    aggregate_key TEXT NOT NULL
        CHECK (length(CAST(aggregate_key AS BLOB)) BETWEEN 1 AND 512),
    request_count INTEGER NOT NULL DEFAULT 1
        CHECK (request_count > 0),
    waiter_count INTEGER NOT NULL DEFAULT 1
        CHECK (waiter_count > 0 AND waiter_count <= request_count),
    state TEXT NOT NULL
        CHECK (state IN ('pending', 'allowed', 'denied', 'canceled', 'expired')),
    decision TEXT NOT NULL DEFAULT ''
        CHECK (decision IN ('', 'allow-once', 'deny')),
    decision_scope TEXT NOT NULL DEFAULT ''
        CHECK (decision_scope IN ('', 'request', 'host_port')),
    decision_reason TEXT NOT NULL DEFAULT ''
        CHECK (length(CAST(decision_reason AS BLOB)) <= 512),
    decision_idempotency_key TEXT NOT NULL DEFAULT ''
        CHECK (length(CAST(decision_idempotency_key AS BLOB)) <= 512),
    created_at_unix_ms INTEGER NOT NULL,
    expires_at_unix_ms INTEGER NOT NULL,
    resolved_at_unix_ms INTEGER NOT NULL DEFAULT 0,
    CHECK (expires_at_unix_ms > created_at_unix_ms),
    CHECK (resolved_at_unix_ms = 0 OR resolved_at_unix_ms >= created_at_unix_ms),
    CHECK (
        (exchange_id = '' AND access_id = ''
            AND plan_revision = 0 AND length(plan_hash) = 0) OR
        (length(CAST(exchange_id AS BLOB)) BETWEEN 1 AND 512
            AND length(CAST(access_id AS BLOB)) BETWEEN 1 AND 128
            AND plan_revision >= 1 AND length(plan_hash) = 32)
    ),
    CHECK (kind <> 'tool_intent' OR plan_revision >= 1),
    -- A network ask is about one connection and says which; every other kind
    -- carries no connection it would never decide.
    CHECK (
        (kind = 'network_ask' AND length(target_host) > 0 AND target_port > 0) OR
        (kind <> 'network_ask' AND target_host = '' AND target_port = 0)
    ),
    -- Only a question about a connection can be remembered.
    CHECK (decision_scope <> 'host_port' OR kind = 'network_ask'),
    CHECK (
        (state = 'pending' AND decision = '' AND decision_scope = ''
            AND decision_reason = '' AND decision_idempotency_key = ''
            AND resolved_at_unix_ms = 0) OR
        (state = 'allowed' AND decision = 'allow-once'
            AND decision_scope <> '' AND decision_reason = ''
            AND length(decision_idempotency_key) >= 16
            AND resolved_at_unix_ms > 0) OR
        (state = 'denied' AND decision = 'deny'
            AND decision_scope <> '' AND length(decision_reason) > 0
            AND length(decision_idempotency_key) >= 16
            AND resolved_at_unix_ms > 0) OR
        (state IN ('canceled', 'expired') AND decision = ''
            AND decision_scope = '' AND length(decision_reason) > 0
            AND decision_idempotency_key = '' AND resolved_at_unix_ms > 0)
    )
) STRICT;

INSERT INTO tool_approvals (
    approval_id, revision, kind, exchange_id, access_id, plan_revision,
    plan_hash, subject_refs_json, subject_labels_json, target_host,
    target_port, aggregate_key, request_count, waiter_count, state, decision,
    decision_scope, decision_reason, decision_idempotency_key,
    created_at_unix_ms, expires_at_unix_ms, resolved_at_unix_ms
)
SELECT
    approval_id, revision, kind, exchange_id, access_id, plan_revision,
    plan_hash, subject_refs_json, subject_labels_json, target_host,
    target_port, aggregate_key, request_count, waiter_count, state, decision,
    decision_scope, decision_reason, decision_idempotency_key,
    created_at_unix_ms, expires_at_unix_ms, resolved_at_unix_ms
FROM tool_approvals_without_client_root;

DROP TABLE tool_approvals_without_client_root;

CREATE UNIQUE INDEX tool_approvals_decision_idempotency
    ON tool_approvals (decision_idempotency_key)
    WHERE decision_idempotency_key <> '';

CREATE INDEX tool_approvals_state_created
    ON tool_approvals (state, created_at_unix_ms DESC);

CREATE UNIQUE INDEX tool_approvals_pending_aggregate
    ON tool_approvals (aggregate_key)
    WHERE state = 'pending';
