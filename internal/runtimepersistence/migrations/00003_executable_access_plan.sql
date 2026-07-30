-- +goose Up
CREATE TABLE access_plan_aggregates (
    access_id TEXT PRIMARY KEY NOT NULL
        REFERENCES access_bindings(access_id) ON DELETE CASCADE,
    format_version INTEGER NOT NULL
        CHECK (format_version = 1),
    payload_json BLOB NOT NULL
        CHECK (length(payload_json) BETWEEN 2 AND 1048576)
) STRICT;
