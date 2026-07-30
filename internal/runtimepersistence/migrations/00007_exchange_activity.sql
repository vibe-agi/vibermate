-- +goose Up
DROP INDEX runtime_activities_latest;

ALTER TABLE runtime_activities RENAME TO runtime_activities_revision_6;

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
            'approval.resolved',
            'exchange.completed'
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

INSERT INTO runtime_activities (
    sequence,
    activity_id,
    occurred_at_unix_ms,
    kind,
    access_id,
    subject_id,
    status,
    reason_code
)
SELECT
    sequence,
    activity_id,
    occurred_at_unix_ms,
    kind,
    access_id,
    subject_id,
    status,
    reason_code
FROM runtime_activities_revision_6;

DROP TABLE runtime_activities_revision_6;

CREATE INDEX runtime_activities_latest
    ON runtime_activities (sequence DESC);
