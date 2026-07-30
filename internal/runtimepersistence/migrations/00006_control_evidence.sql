-- +goose Up
CREATE TABLE runtime_activities (
    sequence INTEGER PRIMARY KEY AUTOINCREMENT,
    activity_id TEXT NOT NULL UNIQUE
        CHECK (length(CAST(activity_id AS BLOB)) BETWEEN 1 AND 512),
    occurred_at_unix_ms INTEGER NOT NULL,
    kind TEXT NOT NULL
        CHECK (kind IN (
            'access.applied',
            'offline_hold.entered',
            'offline_hold.resumed',
            'approval.pending',
            'approval.resolved'
        )),
    access_id TEXT NOT NULL DEFAULT ''
        CHECK (length(CAST(access_id AS BLOB)) <= 128),
    subject_id TEXT NOT NULL
        CHECK (length(CAST(subject_id AS BLOB)) BETWEEN 1 AND 512),
    status TEXT NOT NULL
        CHECK (status IN ('succeeded', 'pending', 'failed', 'canceled')),
    reason_code TEXT NOT NULL DEFAULT ''
        CHECK (length(CAST(reason_code AS BLOB)) <= 512)
) STRICT;

CREATE INDEX runtime_activities_latest
    ON runtime_activities (sequence DESC);

CREATE TABLE tool_approvals (
    approval_id TEXT PRIMARY KEY NOT NULL
        CHECK (length(CAST(approval_id AS BLOB)) BETWEEN 1 AND 512),
    revision INTEGER NOT NULL
        CHECK (revision BETWEEN 1 AND 9223372036854775807),
    exchange_id TEXT NOT NULL
        CHECK (length(CAST(exchange_id AS BLOB)) BETWEEN 1 AND 512),
    access_id TEXT NOT NULL
        CHECK (length(CAST(access_id AS BLOB)) BETWEEN 1 AND 128),
    plan_revision INTEGER NOT NULL
        CHECK (plan_revision BETWEEN 1 AND 9223372036854775807),
    plan_hash BLOB NOT NULL
        CHECK (length(plan_hash) = 32),
    tool_call_ids_json BLOB NOT NULL
        CHECK (length(tool_call_ids_json) BETWEEN 3 AND 65536),
    tool_names_json BLOB NOT NULL
        CHECK (length(tool_names_json) BETWEEN 3 AND 65536),
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

CREATE UNIQUE INDEX tool_approvals_decision_idempotency
    ON tool_approvals (decision_idempotency_key)
    WHERE decision_idempotency_key <> '';

CREATE INDEX tool_approvals_state_created
    ON tool_approvals (state, created_at_unix_ms DESC);
