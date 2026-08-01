-- +goose Up
-- A network ask is decided before any Access is resolved, so it has no
-- Exchange, no Access, and no plan to bind to. Migration 14 added the generic
-- columns but left the tool-intent binding required, which made the new kind
-- unstorable. SQLite cannot relax a CHECK in place, so the table is rebuilt.
--
-- The binding is now all-or-nothing rather than always-present, and a tool
-- intent still requires it. The subject columns are renamed to what they now
-- hold: a tool intent's subjects are tool calls, a network ask's subject is a
-- host and port.
ALTER TABLE tool_approvals RENAME TO tool_approvals_bound;

DROP INDEX tool_approvals_decision_idempotency;
DROP INDEX tool_approvals_state_created;

CREATE TABLE tool_approvals (
    approval_id TEXT PRIMARY KEY NOT NULL
        CHECK (length(CAST(approval_id AS BLOB)) BETWEEN 1 AND 512),
    revision INTEGER NOT NULL
        CHECK (revision BETWEEN 1 AND 9223372036854775807),
    kind TEXT NOT NULL
        CHECK (kind IN ('tool_intent', 'network_ask')),
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
        CHECK (decision_scope IN ('', 'request')),
    decision_reason TEXT NOT NULL DEFAULT ''
        CHECK (length(CAST(decision_reason AS BLOB)) <= 512),
    decision_idempotency_key TEXT NOT NULL DEFAULT ''
        CHECK (length(CAST(decision_idempotency_key AS BLOB)) <= 512),
    created_at_unix_ms INTEGER NOT NULL,
    expires_at_unix_ms INTEGER NOT NULL,
    resolved_at_unix_ms INTEGER NOT NULL DEFAULT 0,
    CHECK (expires_at_unix_ms > created_at_unix_ms),
    CHECK (resolved_at_unix_ms = 0 OR resolved_at_unix_ms >= created_at_unix_ms),
    -- A plan binding is whole or absent. A half-written binding would name an
    -- Access without the revision that makes it meaningful.
    CHECK (
        (exchange_id = '' AND access_id = ''
            AND plan_revision = 0 AND length(plan_hash) = 0) OR
        (length(CAST(exchange_id AS BLOB)) BETWEEN 1 AND 512
            AND length(CAST(access_id AS BLOB)) BETWEEN 1 AND 128
            AND plan_revision >= 1 AND length(plan_hash) = 32)
    ),
    -- A tool intent is decided against a resolved plan, so it always has one.
    CHECK (kind <> 'tool_intent' OR plan_revision >= 1),
    CHECK (
        (state = 'pending' AND decision = '' AND decision_scope = ''
            AND decision_reason = '' AND decision_idempotency_key = ''
            AND resolved_at_unix_ms = 0) OR
        (state = 'allowed' AND decision = 'allow-once'
            AND decision_scope = 'request' AND decision_reason = ''
            AND length(decision_idempotency_key) >= 16
            AND resolved_at_unix_ms > 0) OR
        (state = 'denied' AND decision = 'deny'
            AND decision_scope = 'request' AND length(decision_reason) > 0
            AND length(decision_idempotency_key) >= 16
            AND resolved_at_unix_ms > 0) OR
        (state IN ('canceled', 'expired') AND decision = ''
            AND decision_scope = '' AND length(decision_reason) > 0
            AND decision_idempotency_key = '' AND resolved_at_unix_ms > 0)
    )
) STRICT;

INSERT INTO tool_approvals (
    approval_id, revision, kind, exchange_id, access_id, plan_revision,
    plan_hash, subject_refs_json, subject_labels_json, aggregate_key,
    request_count, waiter_count, state, decision, decision_scope,
    decision_reason, decision_idempotency_key, created_at_unix_ms,
    expires_at_unix_ms, resolved_at_unix_ms
)
SELECT
    approval_id, revision, kind, exchange_id, access_id, plan_revision,
    plan_hash, tool_call_ids_json, tool_names_json, aggregate_key,
    request_count, waiter_count, state, decision, decision_scope,
    decision_reason, decision_idempotency_key, created_at_unix_ms,
    expires_at_unix_ms, resolved_at_unix_ms
FROM tool_approvals_bound;

DROP TABLE tool_approvals_bound;

CREATE UNIQUE INDEX tool_approvals_decision_idempotency
    ON tool_approvals (decision_idempotency_key)
    WHERE decision_idempotency_key <> '';

CREATE INDEX tool_approvals_state_created
    ON tool_approvals (state, created_at_unix_ms DESC);

-- One question is open at a time per aggregate. A second pending record for
-- the same question would show a person the same prompt twice.
CREATE UNIQUE INDEX tool_approvals_pending_aggregate
    ON tool_approvals (aggregate_key)
    WHERE state = 'pending';
