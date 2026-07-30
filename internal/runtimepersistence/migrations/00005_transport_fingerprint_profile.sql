-- +goose Up
ALTER TABLE access_plan_aggregates
RENAME TO access_plan_aggregates_revision_8;

CREATE TABLE access_plan_aggregates (
    access_id TEXT PRIMARY KEY NOT NULL
        REFERENCES access_bindings(access_id) ON DELETE CASCADE,
    format_version INTEGER NOT NULL
        CHECK (format_version = 2),
    payload_json BLOB NOT NULL
        CHECK (length(payload_json) BETWEEN 2 AND 1048576)
) STRICT;

INSERT INTO access_plan_aggregates (
    access_id,
    format_version,
    payload_json
)
SELECT
    access_id,
    2,
    CAST(
        json_set(
            CAST(payload_json AS TEXT),
            '$.profiles[0].transportProfileRef',
            'observed-client-strict-h1'
        )
        AS BLOB
    )
FROM access_plan_aggregates_revision_8;

DROP TABLE access_plan_aggregates_revision_8;
